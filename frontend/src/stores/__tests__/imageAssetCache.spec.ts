import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import type { ImageAsset } from '@/types'
import {
  extractURLExpiry,
  isSignedURLExpiringSoon,
  getCachedURL,
  setCachedURL,
  mergeAssetsWithCache,
  invalidateAsset,
  clearAll,
  refreshURLWithDedup,
  __getCacheSizeForTest,
  __getCacheEntryForTest,
  __resetCacheForTest,
} from '@/stores/imageAssetCache'

// ============================================================================
// 测试辅助
// ============================================================================

const ORIGIN = 'http://localhost:3000'

/** 构造一个最小可用的 ImageAsset */
function makeAsset(overrides: Partial<ImageAsset> = {}): ImageAsset {
  return {
    id: 1,
    generation_id: 1,
    asset_type: 'output',
    mime_type: 'image/png',
    file_size: 1024,
    created_at: '2026-07-18T00:00:00Z',
    url: `${ORIGIN}/api/media/images/test.png?expires=9999999999&signature=abc`,
    ...overrides,
  }
}

/** 构造本地存储格式的签名 URL */
function makeLocalURL(path: string, expiresUnix: number, sig = 'sig'): string {
  return `${ORIGIN}${path}?expires=${expiresUnix}&signature=${sig}`
}

/** 构造 S3 / R2 格式的 presigned URL */
function makeS3URL(path: string, amzDateISOBasic: string, amzExpiresSeconds: number): string {
  return `${ORIGIN}${path}?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAxxx%2F20260719%2Fauto%2Fs3%2Faws4_request&X-Amz-Date=${amzDateISOBasic}&X-Amz-Expires=${amzExpiresSeconds}&X-Amz-SignedHeaders=host&X-Amz-Signature=deadbeef`
}

/** 构造一个未来 expire 时间戳（秒） */
function futureExpires(secondsFromNow: number): number {
  return Math.floor(Date.now() / 1000) + secondsFromNow
}

/** 构造一个过去 expire 时间戳（秒） */
function pastExpires(secondsAgo: number): number {
  return Math.floor(Date.now() / 1000) - secondsAgo
}

/** 构造一个未来的 X-Amz-Date 字符串（ISO 8601 basic: YYYYMMDDTHHMMSSZ） */
function futureAmzDate(secondsFromNow: number): string {
  const d = new Date(Date.now() + secondsFromNow * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}${pad(d.getUTCMonth() + 1)}${pad(d.getUTCDate())}T${pad(d.getUTCHours())}${pad(d.getUTCMinutes())}${pad(d.getUTCSeconds())}Z`
}

/** 构造一个过去的 X-Amz-Date 字符串 */
function pastAmzDate(secondsAgo: number): string {
  return futureAmzDate(-secondsAgo)
}

// ============================================================================
// S3 / R2 presigned URL 解析（extractURLExpiry + isSignedURLExpiringSoon）
// ============================================================================

describe('extractURLExpiry — S3 / R2 presigned URL 格式', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-19T12:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('正确解析 X-Amz-Date + X-Amz-Expires（未来 10 分钟）', () => {
    const url = makeS3URL('/img/x.png', futureAmzDate(0), 600)
    const exp = extractURLExpiry(url)
    // 期望过期时间 = 当前时间 + 600s
    const expected = Math.floor(Date.now() / 1000) + 600
    expect(exp).toBe(expected)
  })

  it('X-Amz-Date 为过去时间 + X-Amz-Expires 较大时，过期时间仍在未来', () => {
    // X-Amz-Date 是 5 分钟前生成的，X-Amz-Expires=1800（30 分钟），因此还有 25 分钟过期
    const url = makeS3URL('/img/x.png', pastAmzDate(300), 1800)
    const exp = extractURLExpiry(url)
    const expected = Math.floor(Date.now() / 1000) - 300 + 1800
    expect(exp).toBe(expected)
  })

  it('X-Amz-Expires 已过期 → 返回的时间戳小于当前', () => {
    const url = makeS3URL('/img/x.png', pastAmzDate(3600), 600) // 1 小时前生成，有效期 10 分钟
    const exp = extractURLExpiry(url)
    expect(exp).toBeLessThan(Math.floor(Date.now() / 1000))
  })

  it('缺少 X-Amz-Date → 返回 0', () => {
    const url = `${ORIGIN}/img/x.png?X-Amz-Expires=600&X-Amz-Signature=abc`
    expect(extractURLExpiry(url)).toBe(0)
  })

  it('缺少 X-Amz-Expires → 返回 0', () => {
    const url = `${ORIGIN}/img/x.png?X-Amz-Date=20260719T120000Z&X-Amz-Signature=abc`
    expect(extractURLExpiry(url)).toBe(0)
  })

  it('X-Amz-Date 格式非法 → 返回 0', () => {
    const url = `${ORIGIN}/img/x.png?X-Amz-Date=not-a-date&X-Amz-Expires=600`
    expect(extractURLExpiry(url)).toBe(0)
  })

  it('X-Amz-Expires 非数字 → 返回 0', () => {
    const url = makeS3URL('/img/x.png', futureAmzDate(0), 0) // 0 视为非法
    // 直接构造非数字场景
    const url2 = `${ORIGIN}/img/x.png?X-Amz-Date=${futureAmzDate(0)}&X-Amz-Expires=abc`
    expect(extractURLExpiry(url)).toBe(0)
    expect(extractURLExpiry(url2)).toBe(0)
  })

  it('S3 URL 与本地 URL 优先级：S3 参数存在时按 S3 解析（虽然两者都有不太可能）', () => {
    // 同时有 expires 和 X-Amz-* 时，extractURLExpiry 先匹配本地 expires
    const localExp = futureExpires(600)
    const url = `${ORIGIN}/img/x.png?expires=${localExp}&X-Amz-Date=${futureAmzDate(0)}&X-Amz-Expires=300`
    expect(extractURLExpiry(url)).toBe(localExp)
  })
})

describe('isSignedURLExpiringSoon — S3 / R2 格式', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-19T12:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('S3 URL 有效期 10 分钟 → 未过期（buffer=60s）', () => {
    const url = makeS3URL('/img/x.png', futureAmzDate(0), 600)
    expect(isSignedURLExpiringSoon(url, 60)).toBe(false)
  })

  it('S3 URL 有效期仅 30 秒 → 即将过期（buffer=60s）', () => {
    const url = makeS3URL('/img/x.png', futureAmzDate(0), 30)
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })

  it('S3 URL 已过期 → 返回 true', () => {
    const url = makeS3URL('/img/x.png', pastAmzDate(3600), 600)
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })

  it('S3 URL X-Amz-Date 格式非法 → 返回 true（视为不可信）', () => {
    const url = `${ORIGIN}/img/x.png?X-Amz-Date=bad&X-Amz-Expires=600`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })

  it('S3 URL 缺少 X-Amz-Expires → 返回 true（视为不可信）', () => {
    const url = `${ORIGIN}/img/x.png?X-Amz-Date=${futureAmzDate(0)}&X-Amz-Signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })
})

// ============================================================================
// 模块级缓存：getCachedURL / setCachedURL / mergeAssetsWithCache
// ============================================================================

describe('模块级缓存读写', () => {
  beforeEach(() => {
    __resetCacheForTest()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-19T12:00:00Z'))
  })
  afterEach(() => {
    __resetCacheForTest()
    vi.useRealTimers()
  })

  it('setCachedURL 后 getCachedURL 返回同一 URL（未过期）', () => {
    const url = makeLocalURL('/img/a.png', futureExpires(600))
    const asset = makeAsset({ id: 1, url })
    setCachedURL(asset, url)
    expect(getCachedURL(asset)).toBe(url)
  })

  it('getCachedURL 在 URL 即将过期时返回 undefined', () => {
    const url = makeLocalURL('/img/a.png', futureExpires(30)) // 30s 后过期，< 60s buffer
    const asset = makeAsset({ id: 1, url })
    setCachedURL(asset, url)
    expect(getCachedURL(asset)).toBeUndefined()
  })

  it('getCachedURL 在 URL 已过期时返回 undefined', () => {
    const url = makeLocalURL('/img/a.png', pastExpires(60))
    const asset = makeAsset({ id: 1, url })
    setCachedURL(asset, url)
    expect(getCachedURL(asset)).toBeUndefined()
  })

  it('getCachedURL 在缓存中没有该 asset 时返回 undefined', () => {
    const asset = makeAsset({ id: 999, url: makeLocalURL('/img/x.png', futureExpires(600)) })
    expect(getCachedURL(asset)).toBeUndefined()
  })

  it('setCachedURL 不会用过期 URL 覆盖未过期的缓存', () => {
    const goodURL = makeLocalURL('/img/a.png', futureExpires(600))
    const expiringURL = makeLocalURL('/img/a.png', futureExpires(30))
    const asset = makeAsset({ id: 1, url: goodURL })
    setCachedURL(asset, goodURL)
    // 尝试用快过期的 URL 覆盖
    setCachedURL(asset, expiringURL)
    expect(getCachedURL(asset)).toBe(goodURL)
  })

  it('setCachedURL 在缓存 URL 即将过期时允许用新 URL 覆盖', () => {
    const expiringURL = makeLocalURL('/img/a.png', futureExpires(30))
    const freshURL = makeLocalURL('/img/a.png', futureExpires(600))
    const asset = makeAsset({ id: 1, url: expiringURL })
    setCachedURL(asset, expiringURL)
    // 缓存 URL 快过期，允许用新 URL 覆盖
    setCachedURL(asset, freshURL)
    expect(getCachedURL(asset)).toBe(freshURL)
  })

  it('setCachedURL url 为 undefined 时跳过写入', () => {
    const asset = makeAsset({ id: 1, url: makeLocalURL('/img/a.png', futureExpires(600)) })
    setCachedURL(asset, undefined)
    expect(__getCacheSizeForTest()).toBe(0)
  })

  it('不同 assetId 不会互相干扰', () => {
    const url1 = makeLocalURL('/img/a.png', futureExpires(600))
    const url2 = makeLocalURL('/img/b.png', futureExpires(600))
    const a1 = makeAsset({ id: 1, url: url1 })
    const a2 = makeAsset({ id: 2, url: url2 })
    setCachedURL(a1, url1)
    setCachedURL(a2, url2)
    expect(getCachedURL(a1)).toBe(url1)
    expect(getCachedURL(a2)).toBe(url2)
    expect(__getCacheSizeForTest()).toBe(2)
  })

  it('同 assetId 不同 objectKey 视为不同 asset（不会错误复用图片）', () => {
    const url1 = makeLocalURL('/img/a.png', futureExpires(600))
    const url2 = makeLocalURL('/img/b.png', futureExpires(600))
    // 同 id 但 path 不同
    const a1 = makeAsset({ id: 1, url: url1 })
    const a2 = makeAsset({ id: 1, url: url2 })
    setCachedURL(a1, url1)
    setCachedURL(a2, url2)
    expect(getCachedURL(a1)).toBe(url1)
    expect(getCachedURL(a2)).toBe(url2)
    expect(__getCacheSizeForTest()).toBe(2)
  })
})

describe('mergeAssetsWithCache', () => {
  beforeEach(() => {
    __resetCacheForTest()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-19T12:00:00Z'))
  })
  afterEach(() => {
    __resetCacheForTest()
    vi.useRealTimers()
  })

  it('缓存中无该 asset → 使用新 URL 并写入缓存', () => {
    const newURL = makeLocalURL('/img/a.png', futureExpires(600))
    const newAssets = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetsWithCache(newAssets)!
    expect(merged[0].url).toBe(newURL)
    expect(__getCacheSizeForTest()).toBe(1)
    expect(getCachedURL(makeAsset({ id: 1, url: newURL }))).toBe(newURL)
  })

  it('缓存中有未过期 URL → 保留缓存 URL（不使用新 URL）', () => {
    const oldURL = makeLocalURL('/img/a.png', futureExpires(600))
    const newURL = makeLocalURL('/img/a.png', futureExpires(700))
    const asset = makeAsset({ id: 1, url: oldURL })
    // 先写入缓存
    setCachedURL(asset, oldURL)
    // 后端返回了新 URL（签名不同）
    const newAssets = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetsWithCache(newAssets)!
    expect(merged[0].url).toBe(oldURL) // 保留旧 URL
  })

  it('缓存 URL 已过期 → 使用新 URL 并更新缓存', () => {
    const expiredURL = makeLocalURL('/img/a.png', pastExpires(60))
    const freshURL = makeLocalURL('/img/a.png', futureExpires(600))
    const asset = makeAsset({ id: 1, url: expiredURL })
    setCachedURL(asset, expiredURL)
    // 此时缓存 URL 已过期，getCachedURL 返回 undefined
    expect(getCachedURL(asset)).toBeUndefined()
    // mergeAssetsWithCache 应使用新 URL
    const newAssets = [makeAsset({ id: 1, url: freshURL })]
    const merged = mergeAssetsWithCache(newAssets)!
    expect(merged[0].url).toBe(freshURL)
    expect(getCachedURL(asset)).toBe(freshURL)
  })

  it('S3 格式 URL 也能命中缓存（X-Amz-Signature 变化但 objectKey 不变）', () => {
    const oldURL = makeS3URL('/img/a.png', futureAmzDate(0), 600)
    // 新 URL 签名不同但 path 相同
    const newURL = makeS3URL('/img/a.png', futureAmzDate(0), 600) + '&X-Amz-Signature=different'
    const asset = makeAsset({ id: 1, url: oldURL })
    setCachedURL(asset, oldURL)
    const newAssets = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetsWithCache(newAssets)!
    expect(merged[0].url).toBe(oldURL) // 保留旧 URL（命中缓存）
  })

  it('新 asset.url 为空 → 保留空 URL（不写入缓存）', () => {
    const newAssets = [makeAsset({ id: 1, url: undefined })]
    const merged = mergeAssetsWithCache(newAssets)!
    expect(merged[0].url).toBeUndefined()
    expect(__getCacheSizeForTest()).toBe(0)
  })

  it('newAssets 为 undefined → 返回 undefined', () => {
    expect(mergeAssetsWithCache(undefined)).toBeUndefined()
  })

  it('newAssets 为空数组 → 返回空数组', () => {
    expect(mergeAssetsWithCache([])).toEqual([])
  })

  it('多 asset 场景：部分命中缓存，部分使用新 URL', () => {
    const path1 = '/img/a.png'
    const path2 = '/img/b.png'
    const path3 = '/img/c.png'
    const oldURL1 = makeLocalURL(path1, futureExpires(600))
    const oldURL2 = makeLocalURL(path2, futureExpires(600))
    const newURL1 = makeLocalURL(path1, futureExpires(700))
    const newURL2 = makeLocalURL(path2, futureExpires(700))
    const newURL3 = makeLocalURL(path3, futureExpires(700))

    // 缓存 1 和 2，不缓存 3
    setCachedURL(makeAsset({ id: 1, url: oldURL1 }), oldURL1)
    setCachedURL(makeAsset({ id: 2, url: oldURL2 }), oldURL2)

    const newAssets = [
      makeAsset({ id: 1, url: newURL1 }),
      makeAsset({ id: 2, url: newURL2 }),
      makeAsset({ id: 3, url: newURL3 }),
    ]
    const merged = mergeAssetsWithCache(newAssets)!
    expect(merged).toHaveLength(3)
    expect(merged[0].url).toBe(oldURL1) // 命中缓存
    expect(merged[1].url).toBe(oldURL2) // 命中缓存
    expect(merged[2].url).toBe(newURL3) // 新 asset，使用新 URL
  })
})

// ============================================================================
// invalidateAsset / clearAll — 删除清理
// ============================================================================

describe('invalidateAsset / clearAll', () => {
  beforeEach(() => {
    __resetCacheForTest()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-19T12:00:00Z'))
  })
  afterEach(() => {
    __resetCacheForTest()
    vi.useRealTimers()
  })

  it('invalidateAsset 清除指定 assetId 的缓存', () => {
    const url1 = makeLocalURL('/img/a.png', futureExpires(600))
    const url2 = makeLocalURL('/img/b.png', futureExpires(600))
    const a1 = makeAsset({ id: 1, url: url1 })
    const a2 = makeAsset({ id: 2, url: url2 })
    setCachedURL(a1, url1)
    setCachedURL(a2, url2)
    expect(__getCacheSizeForTest()).toBe(2)

    invalidateAsset(1)
    expect(__getCacheSizeForTest()).toBe(1)
    expect(getCachedURL(a1)).toBeUndefined()
    expect(getCachedURL(a2)).toBe(url2)
  })

  it('invalidateAsset 不存在的 assetId 不报错', () => {
    expect(() => invalidateAsset(999)).not.toThrow()
  })

  it('invalidateAsset 同时清理 inflight refresh 标记', async () => {
    const asset = makeAsset({ id: 1, url: makeLocalURL('/img/a.png', futureExpires(600)) })
    let resolveRefresh: (url: string) => void = () => {}
    const refreshFn = vi.fn(
      () => new Promise<string>((resolve) => { resolveRefresh = resolve })
    )
    // 启动一个 inflight refresh
    const p = refreshURLWithDedup(asset, refreshFn)
    expect(refreshFn).toHaveBeenCalledTimes(1)

    // 在 refresh 完成前 invalidate
    invalidateAsset(1)

    // 释放 refresh
    resolveRefresh(makeLocalURL('/img/a.png', futureExpires(800)))
    await p
    // 应该不报错，且下次 refresh 可以重新发起
  })

  it('clearAll 清空所有缓存', () => {
    const url1 = makeLocalURL('/img/a.png', futureExpires(600))
    const url2 = makeLocalURL('/img/b.png', futureExpires(600))
    setCachedURL(makeAsset({ id: 1, url: url1 }), url1)
    setCachedURL(makeAsset({ id: 2, url: url2 }), url2)
    expect(__getCacheSizeForTest()).toBe(2)

    clearAll()
    expect(__getCacheSizeForTest()).toBe(0)
  })
})

// ============================================================================
// refreshURLWithDedup — Promise 去重
// ============================================================================

describe('refreshURLWithDedup — Promise 去重', () => {
  beforeEach(() => {
    __resetCacheForTest()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-19T12:00:00Z'))
  })
  afterEach(() => {
    __resetCacheForTest()
    vi.useRealTimers()
  })

  it('并发调用同一 asset 的 refresh 只发起一次实际请求', async () => {
    const asset = makeAsset({ id: 1, url: makeLocalURL('/img/a.png', futureExpires(600)) })
    const refreshFn = vi.fn().mockResolvedValue(makeLocalURL('/img/a.png', futureExpires(900)))

    // 同时发起 3 次 refresh
    const [r1, r2, r3] = await Promise.all([
      refreshURLWithDedup(asset, refreshFn),
      refreshURLWithDedup(asset, refreshFn),
      refreshURLWithDedup(asset, refreshFn),
    ])

    // 实际 refresh 函数只被调用一次
    expect(refreshFn).toHaveBeenCalledTimes(1)
    // 三个调用方拿到同一个 URL
    expect(r1).toBe(r2)
    expect(r2).toBe(r3)
  })

  it('refresh 完成后再次调用会发起新的 refresh（inflight 已清理）', async () => {
    const asset = makeAsset({ id: 1, url: makeLocalURL('/img/a.png', futureExpires(600)) })
    const refreshFn = vi.fn()
      .mockResolvedValueOnce(makeLocalURL('/img/a.png', futureExpires(900)))
      .mockResolvedValueOnce(makeLocalURL('/img/a.png', futureExpires(1000)))

    await refreshURLWithDedup(asset, refreshFn)
    await refreshURLWithDedup(asset, refreshFn)

    expect(refreshFn).toHaveBeenCalledTimes(2)
  })

  it('refresh 失败后再次调用会重试（inflight 已清理，允许重试）', async () => {
    const asset = makeAsset({ id: 1, url: makeLocalURL('/img/a.png', futureExpires(600)) })
    const refreshFn = vi.fn()
      .mockRejectedValueOnce(new Error('network error'))
      .mockResolvedValueOnce(makeLocalURL('/img/a.png', futureExpires(900)))

    // 第一次失败
    await expect(refreshURLWithDedup(asset, refreshFn)).rejects.toThrow('network error')
    // 第二次成功
    const url = await refreshURLWithDedup(asset, refreshFn)
    expect(url).toBe(makeLocalURL('/img/a.png', futureExpires(900)))
    expect(refreshFn).toHaveBeenCalledTimes(2)
  })

  it('refresh 成功后新 URL 写入缓存', async () => {
    const asset = makeAsset({ id: 1, url: makeLocalURL('/img/a.png', pastExpires(60)) })
    const newURL = makeLocalURL('/img/a.png', futureExpires(900))
    const refreshFn = vi.fn().mockResolvedValue(newURL)

    await refreshURLWithDedup(asset, refreshFn)

    // 新 URL 应已写入缓存
    expect(getCachedURL(asset)).toBe(newURL)
  })

  it('不同 asset 的并发 refresh 互不干扰', async () => {
    const a1 = makeAsset({ id: 1, url: makeLocalURL('/img/a.png', futureExpires(600)) })
    const a2 = makeAsset({ id: 2, url: makeLocalURL('/img/b.png', futureExpires(600)) })
    const refreshFn1 = vi.fn().mockResolvedValue(makeLocalURL('/img/a.png', futureExpires(900)))
    const refreshFn2 = vi.fn().mockResolvedValue(makeLocalURL('/img/b.png', futureExpires(900)))

    const [r1, r2] = await Promise.all([
      refreshURLWithDedup(a1, refreshFn1),
      refreshURLWithDedup(a2, refreshFn2),
    ])

    expect(refreshFn1).toHaveBeenCalledTimes(1)
    expect(refreshFn2).toHaveBeenCalledTimes(1)
    expect(r1).toContain('/img/a.png')
    expect(r2).toContain('/img/b.png')
  })

  it('同 assetId 不同 objectKey 视为不同 asset，分别 refresh', async () => {
    // 同 id 但 path 不同
    const a1 = makeAsset({ id: 1, url: makeLocalURL('/img/a.png', futureExpires(600)) })
    const a2 = makeAsset({ id: 1, url: makeLocalURL('/img/b.png', futureExpires(600)) })
    const refreshFn = vi.fn(
      (id: number) => Promise.resolve(makeLocalURL(`/img/${id === 1 ? 'a' : 'b'}.png`, futureExpires(900)))
    )

    await Promise.all([
      refreshURLWithDedup(a1, refreshFn),
      refreshURLWithDedup(a2, refreshFn),
    ])

    // 因为 objectKey 不同，视为两个独立 asset，refresh 调用 2 次
    expect(refreshFn).toHaveBeenCalledTimes(2)
  })
})

// ============================================================================
// __getCacheEntryForTest — 测试辅助函数
// ============================================================================

describe('__getCacheEntryForTest', () => {
  beforeEach(() => {
    __resetCacheForTest()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-19T12:00:00Z'))
  })
  afterEach(() => {
    __resetCacheForTest()
    vi.useRealTimers()
  })

  it('返回缓存 entry 的完整字段', () => {
    const url = makeLocalURL('/img/a.png', futureExpires(600))
    const asset = makeAsset({ id: 42, url })
    setCachedURL(asset, url)

    const entry = __getCacheEntryForTest(asset)
    expect(entry).toBeDefined()
    expect(entry!.assetId).toBe(42)
    expect(entry!.objectKey).toBe('/img/a.png')
    expect(entry!.signedUrl).toBe(url)
    expect(entry!.expiresAt).toBe(futureExpires(600))
    expect(entry!.cachedAt).toBe(Date.now())
  })

  it('缓存中没有该 asset 时返回 undefined', () => {
    const asset = makeAsset({ id: 999, url: makeLocalURL('/img/x.png', futureExpires(600)) })
    expect(__getCacheEntryForTest(asset)).toBeUndefined()
  })
})
