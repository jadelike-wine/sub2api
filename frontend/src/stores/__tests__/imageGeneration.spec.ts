import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import type { ImageAsset } from '@/types'
import {
  extractObjectKey,
  assetStableKey,
  isSignedURLExpiringSoon,
  mergeAssetURLs,
} from '@/stores/imageGeneration'

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

/** 构造一个未来 expire 时间戳（秒） */
function futureExpires(secondsFromNow: number): number {
  return Math.floor(Date.now() / 1000) + secondsFromNow
}

/** 构造一个过去 expire 时间戳（秒） */
function pastExpires(secondsAgo: number): number {
  return Math.floor(Date.now() / 1000) - secondsAgo
}

// ============================================================================
// extractObjectKey
// ============================================================================

describe('extractObjectKey', () => {
  it('从绝对 URL 提取 path 部分', () => {
    const key = extractObjectKey(`${ORIGIN}/api/media/images/foo.png?expires=1&signature=x`)
    expect(key).toBe('/api/media/images/foo.png')
  })

  it('从相对 URL 也能解析（基于 window.location.origin）', () => {
    const key = extractObjectKey('/api/media/images/bar.png?expires=1&signature=x')
    expect(key).toBe('/api/media/images/bar.png')
  })

  it('url 为 undefined 返回空串', () => {
    expect(extractObjectKey(undefined)).toBe('')
  })

  it('url 为空串返回空串', () => {
    expect(extractObjectKey('')).toBe('')
  })

  it('非法 URL 返回空串', () => {
    // 构造一个让 new URL 抛错的字符串（带空格的 scheme）
    expect(extractObjectKey('http://[invalid url')).toBe('')
  })
})

// ============================================================================
// assetStableKey
// ============================================================================

describe('assetStableKey', () => {
  it('返回 id + objectKey 组合', () => {
    const a = makeAsset({ id: 42, url: `${ORIGIN}/api/media/images/x.png?expires=1&signature=x` })
    expect(assetStableKey(a)).toBe('42|/api/media/images/x.png')
  })

  it('url 缺失时退化为 id + 空字符串', () => {
    const a = makeAsset({ id: 42, url: undefined })
    expect(assetStableKey(a)).toBe('42|')
  })

  it('不同 id 产生不同 key', () => {
    const a1 = makeAsset({ id: 1 })
    const a2 = makeAsset({ id: 2 })
    expect(assetStableKey(a1)).not.toBe(assetStableKey(a2))
  })

  it('同 id 不同 path 产生不同 key', () => {
    const a1 = makeAsset({ id: 1, url: `${ORIGIN}/api/media/images/a.png?expires=1&signature=x` })
    const a2 = makeAsset({ id: 1, url: `${ORIGIN}/api/media/images/b.png?expires=1&signature=x` })
    expect(assetStableKey(a1)).not.toBe(assetStableKey(a2))
  })
})

// ============================================================================
// isSignedURLExpiringSoon
// ============================================================================

describe('isSignedURLExpiringSoon', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-18T12:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('expires 在未来且超出 buffer → 返回 false（未过期）', () => {
    const url = `${ORIGIN}/api/media/x.png?expires=${futureExpires(600)}&signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(false)
  })

  it('expires 在 buffer 内 → 返回 true（即将过期）', () => {
    const url = `${ORIGIN}/api/media/x.png?expires=${futureExpires(30)}&signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })

  it('expires 已过期 → 返回 true', () => {
    const url = `${ORIGIN}/api/media/x.png?expires=${pastExpires(60)}&signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })

  it('兼容 exp 参数名', () => {
    const url = `${ORIGIN}/api/media/x.png?exp=${futureExpires(600)}&signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(false)
  })

  it('同时有 expires 和 exp 时优先使用 expires', () => {
    // expires 是未过期，exp 是已过期，应按 expires 判断为未过期
    const url = `${ORIGIN}/api/media/x.png?expires=${futureExpires(600)}&exp=${pastExpires(60)}&signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(false)
  })

  it('缺少 expires 和 exp 参数 → 返回 true', () => {
    const url = `${ORIGIN}/api/media/x.png?signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })

  it('expires 为非合法数字 → 返回 true', () => {
    const url = `${ORIGIN}/api/media/x.png?expires=not-a-number&signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })

  it('expires 为 0 → 返回 true（视为非签名 URL）', () => {
    const url = `${ORIGIN}/api/media/x.png?expires=0&signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(true)
  })

  it('相对 URL 也能正常解析', () => {
    const url = `/api/media/x.png?expires=${futureExpires(600)}&signature=abc`
    expect(isSignedURLExpiringSoon(url, 60)).toBe(false)
  })

  it('非法 URL 字符串 → 返回 true', () => {
    expect(isSignedURLExpiringSoon('http://[invalid url', 60)).toBe(true)
  })
})

// ============================================================================
// mergeAssetURLs
// ============================================================================

describe('mergeAssetURLs', () => {
  // 注意：OLD/NEW_EXPIRES 必须在 fake timer 生效后计算，
  // 否则会用真实时间算 expires，而测试用 fake time 检查过期，导致误判。
  let PATH: string
  let OLD_EXPIRES: number
  let NEW_EXPIRES: number
  let oldURL: string
  let newURL: string

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-18T12:00:00Z'))
    PATH = '/api/media/images/test.png'
    OLD_EXPIRES = futureExpires(600) // 10 分钟后过期，未过期
    NEW_EXPIRES = futureExpires(700)
    oldURL = `${ORIGIN}${PATH}?expires=${OLD_EXPIRES}&signature=old-sig`
    newURL = `${ORIGIN}${PATH}?expires=${NEW_EXPIRES}&signature=new-sig`
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('相同 id、相同 path、不同签名 → 保留旧 URL（命中缓存）', () => {
    const old = [makeAsset({ id: 1, url: oldURL })]
    const next = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBe(oldURL)
  })

  it('相同 id、不同 path → 使用新 URL（objectKey 变化视为新图片）', () => {
    const otherPath = '/api/media/images/other.png'
    const old = [makeAsset({ id: 1, url: `${ORIGIN}${PATH}?expires=${OLD_EXPIRES}&signature=old-sig` })]
    const next = [makeAsset({ id: 1, url: `${ORIGIN}${otherPath}?expires=${NEW_EXPIRES}&signature=new-sig` })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toContain(otherPath)
    expect(merged[0].url).not.toBe(oldURL)
  })

  it('旧 URL 即将过期 → 使用新 URL', () => {
    const expiringOldURL = `${ORIGIN}${PATH}?expires=${futureExpires(30)}&signature=old-sig`
    const old = [makeAsset({ id: 1, url: expiringOldURL })]
    const next = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBe(newURL)
  })

  it('旧 URL 已过期 → 使用新 URL', () => {
    const expiredOldURL = `${ORIGIN}${PATH}?expires=${pastExpires(60)}&signature=old-sig`
    const old = [makeAsset({ id: 1, url: expiredOldURL })]
    const next = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBe(newURL)
  })

  it('旧 URL 缺少 expires 参数 → 使用新 URL', () => {
    const noExpireURL = `${ORIGIN}${PATH}?signature=old-sig`
    const old = [makeAsset({ id: 1, url: noExpireURL })]
    const next = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBe(newURL)
  })

  it('旧 URL expires 非法 → 使用新 URL', () => {
    const badExpireURL = `${ORIGIN}${PATH}?expires=not-a-number&signature=old-sig`
    const old = [makeAsset({ id: 1, url: badExpireURL })]
    const next = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBe(newURL)
  })

  it('新旧 URL 完全相同 → 直接返回新 asset（a.url === old.url）', () => {
    const old = [makeAsset({ id: 1, url: oldURL })]
    const next = [makeAsset({ id: 1, url: oldURL })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBe(oldURL)
    // 应该返回新对象引用（map 的返回值），而不是旧对象
    expect(merged[0]).toBe(next[0])
  })

  it('旧 asset.url 为空 → 使用新 asset', () => {
    const old = [makeAsset({ id: 1, url: undefined })]
    const next = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBe(newURL)
  })

  it('新 asset.url 为空 → 使用新 asset（保留空 URL）', () => {
    const old = [makeAsset({ id: 1, url: oldURL })]
    const next = [makeAsset({ id: 1, url: undefined })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBeUndefined()
  })

  it('newAssets 为空数组 → 返回空数组', () => {
    expect(mergeAssetURLs([makeAsset()], [])).toEqual([])
  })

  it('newAssets 为 undefined → 返回 undefined', () => {
    expect(mergeAssetURLs([makeAsset()], undefined)).toBeUndefined()
  })

  it('oldAssets 为空数组 → 返回新数组', () => {
    const next = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetURLs([], next)!
    expect(merged[0].url).toBe(newURL)
  })

  it('oldAssets 为 undefined → 返回新数组', () => {
    const next = [makeAsset({ id: 1, url: newURL })]
    const merged = mergeAssetURLs(undefined, next)!
    expect(merged[0].url).toBe(newURL)
  })

  it('多 asset 场景：匹配的保留旧 URL，不匹配的使用新 URL', () => {
    const path1 = '/api/media/images/a.png'
    const path2 = '/api/media/images/b.png'
    const path3 = '/api/media/images/c.png' // 仅在新列表中
    const oldURL1 = `${ORIGIN}${path1}?expires=${OLD_EXPIRES}&signature=old1`
    const oldURL2 = `${ORIGIN}${path2}?expires=${OLD_EXPIRES}&signature=old2`
    const newURL1 = `${ORIGIN}${path1}?expires=${NEW_EXPIRES}&signature=new1`
    const newURL2 = `${ORIGIN}${path2}?expires=${NEW_EXPIRES}&signature=new2`
    const newURL3 = `${ORIGIN}${path3}?expires=${NEW_EXPIRES}&signature=new3`

    const old = [
      makeAsset({ id: 1, url: oldURL1 }),
      makeAsset({ id: 2, url: oldURL2 }),
    ]
    const next = [
      makeAsset({ id: 1, url: newURL1 }),
      makeAsset({ id: 2, url: newURL2 }),
      makeAsset({ id: 3, url: newURL3 }),
    ]
    const merged = mergeAssetURLs(old, next)!
    expect(merged).toHaveLength(3)
    expect(merged[0].url).toBe(oldURL1) // 匹配，保留旧
    expect(merged[1].url).toBe(oldURL2) // 匹配，保留旧
    expect(merged[2].url).toBe(newURL3) // 新增，使用新
  })

  it('相对 URL 也能正常解析并匹配', () => {
    const samePath = '/api/media/images/relative.png'
    const old = [makeAsset({ id: 1, url: `${samePath}?expires=${OLD_EXPIRES}&signature=old` })]
    const next = [makeAsset({ id: 1, url: `${samePath}?expires=${NEW_EXPIRES}&signature=new` })]
    const merged = mergeAssetURLs(old, next)!
    expect(merged[0].url).toBe(old[0].url) // 保留旧
  })
})
