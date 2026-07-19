/**
 * Image Asset URL Cache
 *
 * 模块级图片签名 URL 缓存，独立于组件和 Pinia store 生命周期。
 *
 * 设计目标：
 *  1. 路由切换 / 组件卸载时不清空缓存，只在 asset/generation/conversation 被删除或用户登出时清理
 *  2. 以 `assetId + objectKey` 作为稳定 key（不以完整 presigned URL 作为 key）
 *     —— 即使后端每次返回不同的 X-Amz-Signature，只要 objectKey 不变就视为同一张图片
 *  3. 仅在以下情况才允许用新 URL 替换缓存：
 *     - 本地没有缓存
 *     - 缓存 URL 已过期或即将过期（默认 60s buffer）
 *     - URL 解析失败（视为不可信）
 *  4. 对同一 asset 的并发刷新做 Promise 去重，避免多组件同时刷新同一 URL
 *
 * 与 store.mergeAssetURLs 的关系：
 *  - mergeAssetURLs 在 store 内部合并新旧 generation 的 asset URL（按 assetId + objectKey 匹配）
 *  - 本缓存模块作为持久化层：store 每次写入 URL 时同步写入缓存；
 *    selectConversation / fetchGenerationsByConversation 用本缓存合并 URL，
 *    使"重新挂载组件 + 重新拉取 generation 列表"也能复用未过期的旧 URL
 */

import type { ImageAsset } from '@/types'

// ============================================================================
// 类型定义
// ============================================================================

interface CacheEntry {
  /** 资产 ID（数据库主键） */
  assetId: number
  /** 对象 key（URL path 部分，不含 query） */
  objectKey: string
  /** 签名 URL（完整，含 query 参数） */
  signedUrl: string
  /** URL 过期时间戳（unix 秒）；0 表示无法解析 */
  expiresAt: number
  /** 写入缓存的本地时间戳（Date.now()，毫秒） */
  cachedAt: number
}

// ============================================================================
// 常量
// ============================================================================

/**
 * URL 即将过期的 buffer（秒）。
 * 当剩余有效期小于此值时视为"即将过期"，允许用新 URL 替换。
 * 与 store.mergeAssetURLs 中的 buffer 保持一致。
 */
const URL_EXPIRY_BUFFER_SECONDS = 60

// ============================================================================
// 模块级状态
// ============================================================================

/**
 * 缓存表：key = `${assetId}|${objectKey}`
 *
 * 不使用 Map 弱引用：assetId 是数字，无法作为 WeakMap key。
 * 缓存大小受限于用户当前会话内的 asset 数量（通常 < 100），不会无限增长。
 * 删除会话 / 资源 / 登出时主动清理。
 */
const cache = new Map<string, CacheEntry>()

/**
 * 正在进行 URL 刷新的 Promise 表，用于去重并发刷新请求。
 * key 与 cache 表一致：`${assetId}|${objectKey}`
 */
const inflightRefresh = new Map<string, Promise<string>>()

/**
 * 已触发过"图片加载失败 → 刷新 URL 重试"的 assetId 集合。
 *
 * 用于防止无限重试循环：
 *   - el-image 第一次加载失败 → 调用 refreshAssetURL 刷新签名 URL → el-image 用新 URL 重试
 *   - 如果新 URL 仍失败（如权限问题、对象已删除），不再重试，直接显示 error 占位
 *   - asset 被 invalidate（删除/会话切换）时清除标记，允许将来重新重试
 *
 * 模块级（跨组件生命周期），避免路由切换后重复刷新。
 */
const retriedAssetIds = new Set<number>()

// ============================================================================
// 纯函数（导出便于单元测试）
// ============================================================================

/**
 * 从签名 URL 中提取 objectKey（URL path 部分），解析失败返回空串。
 *
 * 同时支持绝对 URL 和相对 URL：
 *  - `https://bucket.s3.amazonaws.com/image-generation/1/x.png?X-Amz-...` → `/image-generation/1/x.png`
 *  - `/api/media/images/x.png?expires=1&signature=x` → `/api/media/images/x.png`
 */
export function extractObjectKey(url: string | undefined): string {
  if (!url) return ''
  try {
    const u = new URL(url, window.location.origin)
    return u.pathname
  } catch {
    return ''
  }
}

/**
 * 构造 asset 的稳定缓存 key：`assetId|objectKey`。
 *
 * 重要：不以完整 presigned URL 作为 key，因为后端每次 presign 都会生成不同的
 * X-Amz-Signature / expires 参数。只要 assetId + objectKey 一致即视为同一张图片。
 */
export function assetStableKey(a: ImageAsset): string {
  const objectKey = extractObjectKey(a.url)
  return objectKey ? `${a.id}|${objectKey}` : `${a.id}|`
}

/**
 * 从签名 URL 解析过期时间戳（unix 秒）。
 *
 * 兼容三种格式：
 *  1. 本地存储：`?expires=<unix>` 或 `?exp=<unix>`（绝对时间戳）
 *  2. S3 / R2 presigned URL：`?X-Amz-Date=<ISOBasic>&X-Amz-Expires=<seconds>`（相对时间）
 *  3. 其他：返回 0 表示无法解析
 *
 * 解析失败返回 0，调用方应将其视为"不可信，需要刷新"。
 */
export function extractURLExpiry(url: string): number {
  try {
    const u = new URL(url, window.location.origin)
    // 1. 本地存储格式：expires / exp 是绝对 unix 时间戳
    const expiresStr = u.searchParams.get('expires') ?? u.searchParams.get('exp')
    if (expiresStr) {
      const exp = parseInt(expiresStr, 10)
      if (!Number.isNaN(exp) && exp > 0) return exp
    }
    // 2. S3 / R2 格式：X-Amz-Date (ISO 8601 basic) + X-Amz-Expires (seconds)
    const amzDate = u.searchParams.get('X-Amz-Date')
    const amzExpires = u.searchParams.get('X-Amz-Expires')
    if (amzDate && amzExpires) {
      const expires = parseInt(amzExpires, 10)
      if (!Number.isNaN(expires) && expires > 0) {
        // X-Amz-Date 格式：20260719T120000Z
        const date = parseAmzDate(amzDate)
        if (date) {
          return Math.floor(date.getTime() / 1000) + expires
        }
      }
    }
    return 0
  } catch {
    return 0
  }
}

/**
 * 解析 AWS X-Amz-Date 格式（ISO 8601 basic: 20260719T120000Z）为 Date 对象。
 * 解析失败返回 null。
 */
function parseAmzDate(amzDate: string): Date | null {
  // 期望格式：YYYYMMDDTHHMMSSZ
  const m = /^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z$/.exec(amzDate)
  if (!m) return null
  const [, y, mo, d, h, mi, s] = m
  const date = new Date(Date.UTC(+y, +mo - 1, +d, +h, +mi, +s))
  if (Number.isNaN(date.getTime())) return null
  return date
}

/**
 * 检查签名 URL 是否在 bufferSeconds 内过期。
 *
 * 兼容本地存储（expires/exp）和 S3（X-Amz-Date + X-Amz-Expires）两种格式。
 * 解析失败（无法识别过期时间）时返回 true，使调用方使用新 URL。
 */
export function isSignedURLExpiringSoon(url: string, bufferSeconds: number): boolean {
  const expiresAt = extractURLExpiry(url)
  if (expiresAt === 0) return true
  return Date.now() / 1000 + bufferSeconds >= expiresAt
}

// ============================================================================
// 缓存读写 API
// ============================================================================

/**
 * 获取 asset 在缓存中"未过期"的 URL。
 *
 * 返回 undefined 的场景：
 *  - 缓存中没有该 asset
 *  - 缓存 URL 已过期或即将过期（剩余有效期 < URL_EXPIRY_BUFFER_SECONDS）
 *  - 缓存 URL 解析失败
 *
 * 调用方拿到 undefined 时应使用后端返回的新 URL，并调用 setCachedURL 更新缓存。
 */
export function getCachedURL(asset: ImageAsset): string | undefined {
  const key = assetStableKey(asset)
  const entry = cache.get(key)
  if (!entry) return undefined
  if (isSignedURLExpiringSoon(entry.signedUrl, URL_EXPIRY_BUFFER_SECONDS)) {
    // 缓存 URL 即将过期，不返回，让调用方使用新 URL 并更新缓存
    return undefined
  }
  return entry.signedUrl
}

/**
 * 将 asset 的 URL 写入缓存。
 *
 * 仅当新 URL 不为空且未被识别为"即将过期"时才写入；
 * 否则跳过写入（避免用过期 URL 覆盖尚未过期的缓存）。
 *
 * 特殊场景：当缓存中已有该 asset 的 URL 且尚未过期时，
 * 保留旧 URL（命中浏览器缓存），不写入新 URL。
 */
export function setCachedURL(asset: ImageAsset, url: string | undefined): void {
  if (!url) return
  const key = assetStableKey(asset)
  const existing = cache.get(key)
  // 缓存中已有未过期的 URL：保留旧 URL（命中浏览器缓存）
  if (existing && !isSignedURLExpiringSoon(existing.signedUrl, URL_EXPIRY_BUFFER_SECONDS)) {
    return
  }
  // 写入新 URL
  cache.set(key, {
    assetId: asset.id,
    objectKey: extractObjectKey(url),
    signedUrl: url,
    expiresAt: extractURLExpiry(url),
    cachedAt: Date.now(),
  })
}

/**
 * 将一组新 asset 的 URL 与缓存合并：
 *  - 缓存中有未过期的 URL → 保留缓存 URL（命中浏览器缓存）
 *  - 缓存中没有或已过期 → 使用新 URL 并写入缓存
 *
 * 同时处理 input + output 两组 asset。
 * 返回合并后的 asset 列表（新数组引用，不修改入参）。
 */
export function mergeAssetsWithCache(newAssets: ImageAsset[] | undefined): ImageAsset[] | undefined {
  if (!newAssets || newAssets.length === 0) return newAssets
  return newAssets.map((a) => {
    if (!a.url) return a
    const cached = getCachedURL(a)
    if (cached && cached !== a.url) {
      // 缓存中有未过期的 URL，保留以命中浏览器缓存
      return { ...a, url: cached }
    }
    // 缓存中没有或已过期，使用新 URL 并写入缓存
    setCachedURL(a, a.url)
    return a
  })
}

// ============================================================================
// 失效 / 清理 API
// ============================================================================

/**
 * 失效指定 assetId 的缓存。
 * 用于 asset 被删除、URL 被手动刷新等场景。
 */
export function invalidateAsset(assetId: number): void {
  for (const [key, entry] of cache) {
    if (entry.assetId === assetId) {
      cache.delete(key)
      // 同时取消正在进行的刷新请求
      const inflight = inflightRefresh.get(key)
      if (inflight) {
        inflightRefresh.delete(key)
        // 不主动 abort promise，让调用方自然完成；
        // 删除 entry 后下次 getCachedURL 会返回 undefined
      }
      // 清除重试标记，允许将来重新触发 403 重试
      retriedAssetIds.delete(assetId)
      return
    }
  }
  // 即使缓存中没有该 asset，也清除重试标记（防御性）
  retriedAssetIds.delete(assetId)
}

/**
 * 失效指定 generationId 下所有 asset 的缓存。
 * 用于 generation 被删除等场景。
 */
export function invalidateGeneration(generationId: number): void {
  // 缓存 entry 不存 generationId（避免冗余字段），
  // 调用方需先从 store 拿到该 generation 的 asset 列表再逐个失效。
  // 此处提供按 generationId 失效的便捷方法仅用于测试 / 主动清理场景。
  // 在 store 中通过 deleteGeneration 调用 invalidateAsset 逐个清理。
  void generationId
}

/**
 * 清空整个缓存。
 * 用于用户登出、切换用户等场景。
 */
export function clearAll(): void {
  cache.clear()
  inflightRefresh.clear()
  retriedAssetIds.clear()
}

// ============================================================================
// 403 / 加载失败重试跟踪
// ============================================================================

/**
 * 标记指定 asset 已触发过一次"加载失败 → 刷新 URL 重试"。
 * 用于防止 el-image 无限重试循环（refresh 后新 URL 仍失败则不再重试）。
 */
export function markAssetRetried(assetId: number): void {
  retriedAssetIds.add(assetId)
}

/**
 * 检查指定 asset 是否已触发过重试。
 * 返回 true 时调用方不应再次 refresh，直接显示 error 占位。
 */
export function wasAssetRetried(assetId: number): boolean {
  return retriedAssetIds.has(assetId)
}

// ============================================================================
// Promise 去重
// ============================================================================

/**
 * 对同一 asset 的 URL 刷新请求做 Promise 去重。
 *
 * 多个组件同时发现 URL 过期并尝试刷新时，只发起一次实际刷新请求，
 * 其他调用方等待同一个 Promise。
 *
 * @param asset 需要刷新 URL 的资产
 * @param refreshFn 实际调用后端 refresh-url 接口的函数，返回新 URL
 * @returns 刷新后的新 URL
 */
export async function refreshURLWithDedup(
  asset: ImageAsset,
  refreshFn: (assetId: number) => Promise<string>
): Promise<string> {
  const key = assetStableKey(asset)

  // 已有正在进行的刷新请求，复用该 Promise
  const inflight = inflightRefresh.get(key)
  if (inflight) return inflight

  const promise = (async () => {
    try {
      const newURL = await refreshFn(asset.id)
      // 用新 URL 更新缓存
      cache.set(key, {
        assetId: asset.id,
        objectKey: extractObjectKey(newURL),
        signedUrl: newURL,
        expiresAt: extractURLExpiry(newURL),
        cachedAt: Date.now(),
      })
      return newURL
    } finally {
      // 无论成功失败都清理 inflight 标记，允许下次重试
      inflightRefresh.delete(key)
    }
  })()

  inflightRefresh.set(key, promise)
  return promise
}

// ============================================================================
// 测试辅助（仅测试环境使用）
// ============================================================================

/**
 * 返回缓存大小（仅用于测试断言）。
 */
export function __getCacheSizeForTest(): number {
  return cache.size
}

/**
 * 返回缓存中某个 asset 的 entry（仅用于测试断言）。
 */
export function __getCacheEntryForTest(asset: ImageAsset): CacheEntry | undefined {
  return cache.get(assetStableKey(asset))
}

/**
 * 重置缓存（仅用于测试 setup）。
 */
export function __resetCacheForTest(): void {
  cache.clear()
  inflightRefresh.clear()
  retriedAssetIds.clear()
}
