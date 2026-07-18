/**
 * Cloudflare Worker: R2 Public Access Policy
 *
 * 部署此 Worker 到绑定 R2 bucket 的自定义域名（Public Base URL）上，
 * 作为应用代码无法在 bucket 级别强制执行的安全边界：
 *   - 允许匿名 GET/HEAD 访问图片前缀（agnes-chat/、image-generation/）
 *   - 显式拒绝 backups/ 前缀的匿名访问（数据库备份绝不可公开）
 *   - 拒绝所有其他路径及写方法（PUT/DELETE/POST）
 *
 * 部署后，后端 UpdateS3Config 会通过探测 backups/.policy-probe-<uuid> 验证此策略生效
 * （期望返回 403；若返回 404 说明 bucket 仍公开可读，配置将被拒绝保存）。
 *
 * 部署方式：
 *   1. 复制 wrangler.toml.example 为 wrangler.toml，填入 bucket 名称与自定义域名
 *   2. npm create cloudflare@latest -- r2-access-policy（或使用现有 wrangler 项目）
 *   3. wrangler deploy
 *   4. 在 Cloudflare Dashboard 将自定义域名（如 cdn.example.com）的路由指向此 Worker
 *   5. 在应用后台 S3 配置中填写 Public Base URL = https://cdn.example.com，保存时自动验证
 *
 * 安全说明：
 *   - 备份下载仍走 R2 API presigned URL（不经此 Worker），不受影响。
 *   - 此 Worker 是 PublicBaseURL 场景下 backups/ 隔离的唯一可信边界，必须部署。
 *   - 若不使用 PublicBaseURL（bucket 私有 + presigned），则无需此 Worker。
 */

// 允许匿名读取的前缀（与后端 AgnesChatPrefix / ImageGenerationPrefix 常量保持一致）。
// 默认拒绝所有其他前缀，包括 backups/（数据库备份绝不可公开）。
const ALLOWED_PREFIXES = ['agnes-chat/', 'image-generation/'];

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    // pathname 以 / 开头，去掉后得到 R2 object key
    const key = decodeURIComponent(url.pathname.replace(/^\//, ''));

    // 仅允许匿名 GET / HEAD（图片直链只读）
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return new Response('Method Not Allowed', { status: 405 });
    }

    // 默认拒绝：仅显式允许的前缀放行
    const allowed = ALLOWED_PREFIXES.some((p) => key.startsWith(p));
    if (!allowed) {
      // 403 明确表示访问被策略拒绝（后端探测依赖此状态码区分“被拒绝”与“公开可读但 key 不存在→404”）
      return new Response('Forbidden', { status: 403 });
    }

    // 从 R2 读取对象
    const object = await env.R2_BUCKET.get(key);
    if (object === null) {
      return new Response('Not Found', { status: 404 });
    }

    const headers = new Headers();
    object.writeHttpMetadata(headers);
    // 与后端 presigned URL 最短有效期对齐（30 分钟），避免缓存过期后仍返回旧 URL 指向的对象
    headers.set('Cache-Control', 'public, max-age=1800');
    headers.set('ETag', object.httpEtag);

    // HEAD 请求只返回头，不返回 body
    if (request.method === 'HEAD') {
      return new Response(null, { headers });
    }
    return new Response(object.body, { headers });
  },
};
