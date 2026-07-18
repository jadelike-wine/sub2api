#!/usr/bin/env node
// verify-policy.mjs — 通过 Cloudflare API 权威获取 R2 bucket 的所有公开入口，
// 并验证它们都拒绝 backups/ 匿名读取。
//
// === 为什么需要此脚本 ===
//
// 应用后端的 AdditionalPublicBaseURLs 字段是声明式的——管理员可能漏报公开入口，
// 后端无法发现未声明的域名（如遗忘禁用的 r2.dev、新增的 custom domain）。
// 此脚本通过 Cloudflare REST API 权威获取 bucket 的：
//   1. R2 Public Development URL（*.r2.dev）状态——若启用则直接 FAIL
//   2. 所有已绑定的 custom domain——逐一探测确认 backups/ 被拒绝
//
// 输入来自 Cloudflare API（权威源），不依赖人工填写，关闭了"漏报"盲区。
//
// === 用法 ===
//
//   必需环境变量：
//     CF_ACCOUNT_ID     Cloudflare 账户 ID（Dashboard 右下角）
//     CF_API_TOKEN      Cloudflare API Token（需 R2 Storage Read 权限）
//     R2_BUCKET_NAME    要验证的 R2 bucket 名称
//
//   可选环境变量：
//     CF_JURISDICTION   R2 bucket 司法辖区（"default" | "eu" | "fedramp"），默认 "default"
//     EXTRA_PUBLIC_URLS 额外要探测的 URL（逗号分隔；用于非 Cloudflare 入口或第三方 CDN）
//
//   运行：
//     CF_ACCOUNT_ID=... CF_API_TOKEN=... R2_BUCKET_NAME=... node verify-policy.mjs
//
//   CI/定期巡检建议：将此脚本加入部署后钩子和每日 cron，任何失败立即告警。
//
// === 退出码 ===
//   0  所有公开入口都拒绝 backups/（策略生效）
//   1  存在暴露 backups/ 的入口，或 r2.dev 未禁用，或 API 调用失败
//   2  参数缺失/配置错误

import { randomUUID } from 'node:crypto';

const ACCOUNT_ID = process.env.CF_ACCOUNT_ID;
const API_TOKEN = process.env.CF_API_TOKEN;
const BUCKET_NAME = process.env.R2_BUCKET_NAME;
const JURISDICTION = process.env.CF_JURISDICTION || 'default';
const EXTRA_URLS = (process.env.EXTRA_PUBLIC_URLS || '')
  .split(',')
  .map((s) => s.trim())
  .filter((s) => s.length > 0);

if (!ACCOUNT_ID || !API_TOKEN || !BUCKET_NAME) {
  console.error('Missing required environment variables: CF_ACCOUNT_ID, CF_API_TOKEN, R2_BUCKET_NAME');
  console.error('');
  console.error('Set them and rerun:');
  console.error('  CF_ACCOUNT_ID=... CF_API_TOKEN=... R2_BUCKET_NAME=... node verify-policy.mjs');
  console.error('');
  console.error('CF_API_TOKEN needs "Workers R2 Storage Read" permission (Dashboard → My Profile → API Tokens).');
  process.exit(2);
}

const API_BASE = `https://api.cloudflare.com/client/v4/accounts/${ACCOUNT_ID}/r2/buckets/${BUCKET_NAME}`;
const PROBE_TIMEOUT_MS = 5000;
const r2Headers = {
  Authorization: `Bearer ${API_TOKEN}`,
  'cf-r2-jurisdiction': JURISDICTION,
};

let allPassed = true;

// ─── 工具函数 ───

async function cfFetch(path) {
  const resp = await fetch(`${API_BASE}${path}`, { headers: r2Headers });
  const body = await resp.json().catch(() => ({}));
  if (!resp.ok || body.success === false) {
    const errMsg = body?.errors?.[0]?.message || `HTTP ${resp.status}`;
    const errCode = body?.errors?.[0]?.code || resp.status;
    return { ok: false, status: resp.status, error: `${errCode}: ${errMsg}`, body };
  }
  return { ok: true, status: resp.status, body };
}

async function probeBackupsAccess(baseUrl) {
  // 与后端 verifyBackupPrefixNotPublic 一致：GET + Range，期望 403/401
  const trimmed = baseUrl.replace(/\/+$/, '');
  const probeKey = `backups/.policy-probe-${randomUUID()}`;
  const probeUrl = `${trimmed}/${probeKey}`;

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);

  let status = -1;
  let err = null;
  try {
    const resp = await fetch(probeUrl, {
      method: 'GET',
      headers: { Range: 'bytes=0-0' },
      signal: controller.signal,
      redirect: 'manual',
    });
    status = resp.status;
  } catch (e) {
    err = e;
  } finally {
    clearTimeout(timer);
  }

  return { trimmed, probeUrl, status, err };
}

function reportProbeResult({ trimmed, probeUrl, status, err }) {
  if (err) {
    console.log(`  ❌ ${trimmed}\n     UNREACHABLE: ${err.message}\n     → 无法验证；确保该域名可达且已部署 Worker。`);
    return false;
  }
  if (status === 403 || status === 401) {
    console.log(`  ✅ ${trimmed}\n     → ${status} (access denied, policy effective)`);
    return true;
  }
  if (status === 404) {
    console.log(`  ❌ ${trimmed}\n     → ${status} (anonymous access ALLOWED, backups/ is publicly readable!)\n     → 部署 r2-access-policy.js 或移除该公开入口。`);
    return false;
  }
  if (status === 200 || status === 206) {
    console.log(`  ❌ ${trimmed}\n     → ${status} (content served — backups/ is publicly readable!)\n     → 部署 r2-access-policy.js 或移除该公开入口。`);
    return false;
  }
  console.log(`  ❌ ${trimmed}\n     → ${status} (unexpected — cannot verify, fail-closed)\n     → 部署 r2-access-policy.js 或移除该公开入口。`);
  return false;
}

// ─── 主流程 ───

console.log(`R2 bucket: ${BUCKET_NAME} (jurisdiction: ${JURISDICTION})`);
console.log('');

// 步骤 1：检查 r2.dev Public Development URL（managed domain）状态
// Cloudflare API: GET /accounts/{account_id}/r2/buckets/{bucket_name}/domains/managed
// 返回 { bucketId, domain, enabled }——enabled: true 表示 r2.dev 已启用（必须禁用）
console.log('Step 1: Checking R2 Public Development URL (*.r2.dev) status...');
const managedResp = await cfFetch('/domains/managed');
let r2DevDomain = null;
if (!managedResp.ok) {
  // 404 或 "not found" 通常表示该 bucket 从未启用 r2.dev——视为已禁用
  if (managedResp.status === 404 || /not found|does not exist/i.test(managedResp.error)) {
    console.log('  ✅ r2.dev Public Development URL not configured (OK)');
  } else {
    console.log(`  ❌ Failed to query r2.dev status: ${managedResp.error}`);
    console.log('     → Check CF_API_TOKEN has "Workers R2 Storage Read" permission.');
    allPassed = false;
  }
} else {
  const info = managedResp.body?.result || {};
  r2DevDomain = info.domain || null;
  if (info.enabled) {
    console.log(`  ❌ r2.dev Public Development URL is ENABLED: https://${r2DevDomain}`);
    console.log('     → This bypasses the Worker entirely. Disable it immediately:');
    console.log('       Dashboard → R2 → <bucket> → Settings → Public Development URL → OFF');
    console.log('       Or via API: PUT /accounts/{id}/r2/buckets/{bucket}/domains/managed {"enabled":false}');
    allPassed = false;
  } else {
    console.log(`  ✅ r2.dev Public Development URL is disabled${r2DevDomain ? ` (domain: ${r2DevDomain})` : ''}`);
  }
}
console.log('');

// 步骤 2：列出所有 custom domain（权威源，不依赖人工声明）
// Cloudflare API: GET /accounts/{account_id}/r2/buckets/{bucket_name}/domains/custom
// 返回 { domains: [{ domain, enabled, status: { ownership, ssl }, minTLS, zoneId, zoneName }] }
console.log('Step 2: Listing custom domains from Cloudflare API (authoritative source)...');
const customResp = await cfFetch('/domains/custom');
const customDomains = [];
if (!customResp.ok) {
  console.log(`  ❌ Failed to list custom domains: ${customResp.error}`);
  console.log('     → Check CF_API_TOKEN has "Workers R2 Storage Read" permission.');
  allPassed = false;
} else {
  const domains = customResp.body?.result?.domains || [];
  if (domains.length === 0) {
    console.log('  ℹ️  No custom domains bound to this bucket.');
  } else {
    for (const d of domains) {
      console.log(`  • ${d.domain} (enabled=${d.enabled}, ownership=${d.status?.ownership || '?'}, ssl=${d.status?.ssl || '?'})`);
      // 仅探测 enabled=true 且 ownership/ssl 就绪的域名（active 状态）
      // 未就绪的域名无法公开访问，但也提示管理员检查
      if (d.enabled && d.status?.ownership === 'active') {
        customDomains.push(`https://${d.domain}`);
      } else {
        console.log(`    ⚠️  Skipped (not active) — verify this is intentional.`);
      }
    }
  }
}
console.log('');

// 步骤 3：探测所有 active 的 custom domain
console.log(`Step 3: Probing ${customDomains.length} active custom domain(s) for backups/ access...`);
for (const url of customDomains) {
  const result = await probeBackupsAccess(url);
  if (!reportProbeResult(result)) {
    allPassed = false;
  }
}
console.log('');

// 步骤 4：探测 EXTRA_PUBLIC_URLS（用于非 Cloudflare 入口或第三方 CDN）
if (EXTRA_URLS.length > 0) {
  console.log(`Step 4: Probing ${EXTRA_URLS.length} extra public URL(s) from EXTRA_PUBLIC_URLS...`);
  for (const url of EXTRA_URLS) {
    const result = await probeBackupsAccess(url);
    if (!reportProbeResult(result)) {
      allPassed = false;
    }
  }
  console.log('');
}

// ─── 汇总 ───
console.log('─── Summary ───');
if (allPassed) {
  console.log('✅ All public entry points correctly deny backups/.');
  console.log('   r2.dev disabled, all custom domains probed and protected.');
  process.exit(0);
} else {
  console.log('❌ One or more checks failed. Fix before enabling PublicBaseURL.');
  console.log('   See deploy/cloudflare-worker/README.md for remediation steps.');
  process.exit(1);
}
