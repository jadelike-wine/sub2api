/**
 * 集成测试：路由切换 → 重新挂载 → 图片 URL 缓存复用
 *
 * 覆盖用户提出的场景：
 *   2. 路由重新挂载后可以复用缓存
 *   3. 已完成 generation 不再轮询
 *   5. 签名 URL 过期或返回 403 时能够刷新并重试一次
 *   7. 删除会话或资源后能够清除对应缓存
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

import type { ImageAsset, ImageGeneration } from '@/types'
import { useImageGenerationStore } from '@/stores/imageGeneration'
import {
  __resetCacheForTest,
  __getCacheSizeForTest,
  getCachedURL,
  wasAssetRetried,
} from '@/stores/imageAssetCache'

// ============================================================================
// Mock imageGeneration API
// ============================================================================

const ORIGIN = 'http://localhost:3000'

const mockListConversations = vi.fn()
const mockGetConversation = vi.fn()
const mockListGenerationsByConversation = vi.fn()
const mockRefreshAssetURL = vi.fn()
const mockDeleteConversation = vi.fn()
const mockDeleteGeneration = vi.fn()
const mockGetGeneration = vi.fn()

vi.mock('@/api/imageGeneration', () => ({
  default: {
    listConversations: (...args: any[]) => mockListConversations(...args),
    getConversation: (...args: any[]) => mockGetConversation(...args),
    listGenerationsByConversation: (...args: any[]) => mockListGenerationsByConversation(...args),
    refreshAssetURL: (...args: any[]) => mockRefreshAssetURL(...args),
    deleteConversation: (...args: any[]) => mockDeleteConversation(...args),
    deleteGeneration: (...args: any[]) => mockDeleteGeneration(...args),
    getGeneration: (...args: any[]) => mockGetGeneration(...args),
  },
}))

// ============================================================================
// 测试辅助
// ============================================================================

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

function makeGen(overrides: Partial<ImageGeneration> = {}): ImageGeneration {
  return {
    id: 1,
    conversation_id: 1,
    provider: 'agnes',
    generation_type: 'text_to_image',
    prompt: 'test',
    size: '2K',
    ratio: '1:1',
    status: 'succeeded',
    duration_ms: 1000,
    created_at: '2026-07-18T00:00:00Z',
    updated_at: '2026-07-18T00:00:00Z',
    ...overrides,
  }
}

function futureExpires(secondsFromNow: number): number {
  return Math.floor(Date.now() / 1000) + secondsFromNow
}

function pastExpires(secondsAgo: number): number {
  return Math.floor(Date.now() / 1000) - secondsAgo
}

function makeLocalURL(path: string, expiresUnix: number): string {
  return `${ORIGIN}${path}?expires=${expiresUnix}&signature=sig`
}

// ============================================================================
// 测试套件
// ============================================================================

describe('路由切换 → 重新挂载 → 缓存复用', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    __resetCacheForTest()
    vi.clearAllMocks()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-19T12:00:00Z'))
  })
  afterEach(() => {
    __resetCacheForTest()
    vi.useRealTimers()
  })

  // --- 场景 2：路由重新挂载后可以复用缓存 ---

  it('场景2：路由切换回来重新拉取 generation 列表时，未过期的 URL 被保留（不重新下载）', async () => {
    const store = useImageGenerationStore()
    const path = '/api/media/images/output.png'
    const firstURL = makeLocalURL(path, futureExpires(600)) // 10 分钟后过期

    // 模拟后端第一次返回的 generation 列表
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: firstURL })],
      }),
    ])

    // 首次进入路由：选中会话 → 拉取 generation 列表
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    await store.selectConversation(1)

    // 验证：第一次拉取后 URL 已写入缓存
    expect(store.generations[0].output_assets![0].url).toBe(firstURL)
    expect(__getCacheSizeForTest()).toBe(1)

    // 模拟路由切换离开（onUnmounted）：只停止轮询，不清空 store / 缓存
    store.stopAllPolling()

    // 模拟后端第二次返回的 generation 列表（签名 URL 不同，但 objectKey 相同）
    const secondURL = makeLocalURL(path, futureExpires(700)) // 不同的签名
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: secondURL })],
      }),
    ])

    // 路由切换回来：再次选中会话（模拟组件重新挂载后用户重新进入会话）
    await store.selectConversation(1)

    // 关键断言：URL 应保留为第一次的 URL（命中缓存），而不是用新的 secondURL
    expect(store.generations[0].output_assets![0].url).toBe(firstURL)
    expect(store.generations[0].output_assets![0].url).not.toBe(secondURL)
  })

  it('场景2 补充：路由切换回来后，缓存中无该 asset 时使用新 URL', async () => {
    const store = useImageGenerationStore()
    const path = '/api/media/images/output.png'
    const newURL = makeLocalURL(path, futureExpires(600))

    // 缓存为空（首次进入或缓存已被清除）
    expect(__getCacheSizeForTest()).toBe(0)

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: newURL })],
      }),
    ])

    await store.selectConversation(1)

    expect(store.generations[0].output_assets![0].url).toBe(newURL)
    expect(__getCacheSizeForTest()).toBe(1)
  })

  // --- 场景 3：已完成 generation 不再轮询 ---

  it('场景3：succeeded 状态的 generation 选中会话后不启动轮询', async () => {
    const store = useImageGenerationStore()
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({ id: 1, status: 'succeeded' }),
    ])

    await store.selectConversation(1)

    // succeeded 是终态，不应启动轮询
    expect(store.hasActiveGeneration).toBe(false)
    expect(store.activeGenerationIds.size).toBe(0)
  })

  it('场景3：failed 状态的 generation 选中会话后不启动轮询', async () => {
    const store = useImageGenerationStore()
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({ id: 1, status: 'failed' }),
    ])

    await store.selectConversation(1)

    expect(store.hasActiveGeneration).toBe(false)
  })

  it('场景3：canceled 状态的 generation 选中会话后不启动轮询', async () => {
    const store = useImageGenerationStore()
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({ id: 1, status: 'canceled' }),
    ])

    await store.selectConversation(1)

    expect(store.hasActiveGeneration).toBe(false)
  })

  it('场景3：processing 状态的 generation 选中会话后启动轮询', async () => {
    const store = useImageGenerationStore()
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({ id: 1, status: 'processing' }),
    ])

    await store.selectConversation(1)

    // processing 是运行态，应启动轮询
    expect(store.hasActiveGeneration).toBe(true)
    expect(store.activeGenerationIds.has(1)).toBe(true)

    // 清理：停止轮询避免影响后续测试
    store.stopAllPolling()
  })

  it('场景3 补充：路由切换离开（stopAllPolling）后再回来，succeeded 仍不轮询', async () => {
    const store = useImageGenerationStore()
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({ id: 1, status: 'succeeded' }),
    ])

    await store.selectConversation(1)
    expect(store.hasActiveGeneration).toBe(false)

    // 模拟路由切换离开
    store.stopAllPolling()

    // 路由切换回来：重新拉取列表
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({ id: 1, status: 'succeeded' }),
    ])
    await store.selectConversation(1)

    // 仍然是 succeeded，不应启动轮询
    expect(store.hasActiveGeneration).toBe(false)
  })

  // --- 场景 4：签名 URL 未过期时不刷新 ---

  it('场景4：未过期的 URL 在重新拉取列表时被保留（不调用 refreshAssetURL）', async () => {
    const store = useImageGenerationStore()
    const path = '/api/media/images/x.png'
    const freshURL = makeLocalURL(path, futureExpires(600))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: freshURL })],
      }),
    ])

    await store.selectConversation(1)

    // 重新拉取列表（URL 仍然 fresh）
    const freshURL2 = makeLocalURL(path, futureExpires(700))
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: freshURL2 })],
      }),
    ])
    await store.selectConversation(1)

    // 未过期 → 保留旧 URL
    expect(store.generations[0].output_assets![0].url).toBe(freshURL)
    // 未触发 refreshAssetURL 接口
    expect(mockRefreshAssetURL).not.toHaveBeenCalled()
  })

  // --- 场景 5：签名 URL 过期或 403 时刷新并重试一次 ---

  it('场景5a：缓存 URL 已过期时，重新拉取列表使用后端返回的新 URL', async () => {
    const store = useImageGenerationStore()
    const path = '/api/media/images/x.png'
    const expiredURL = makeLocalURL(path, pastExpires(120)) // 2 分钟前过期
    const freshURL = makeLocalURL(path, futureExpires(600))

    // 先写入过期 URL 到缓存
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: expiredURL })],
      }),
    ])
    await store.selectConversation(1)

    // 推进时间使 URL 过期
    vi.setSystemTime(new Date('2026-07-19T12:30:00Z'))

    // 重新拉取列表，后端返回 fresh URL
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: freshURL })],
      }),
    ])
    await store.selectConversation(1)

    // 过期 → 使用新 URL
    expect(store.generations[0].output_assets![0].url).toBe(freshURL)
  })

  it('场景5b：图片加载失败（403）时调用 refreshAssetURL 刷新 URL 一次', async () => {
    const store = useImageGenerationStore()
    const path = '/api/media/images/x.png'
    const originalURL = makeLocalURL(path, futureExpires(600))
    const refreshedURL = makeLocalURL(path, futureExpires(900))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: originalURL })],
      }),
    ])
    await store.selectConversation(1)

    // 模拟 el-image 加载失败 → 调用 handleImageLoadError
    mockRefreshAssetURL.mockResolvedValueOnce({ url: refreshedURL })
    const refreshed = await store.handleImageLoadError(10)

    // 第一次失败应触发 refresh
    expect(refreshed).toBe(true)
    expect(mockRefreshAssetURL).toHaveBeenCalledTimes(1)
    // store 中 asset.url 已更新为新的 URL
    expect(store.generations[0].output_assets![0].url).toBe(refreshedURL)
    // asset 已被标记为"已重试"
    expect(wasAssetRetried(10)).toBe(true)
  })

  it('场景5c：图片第二次加载失败时不再 refresh（避免无限循环）', async () => {
    const store = useImageGenerationStore()
    const originalURL = makeLocalURL('/api/media/x.png', futureExpires(600))
    const refreshedURL = makeLocalURL('/api/media/x.png', futureExpires(900))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: originalURL })],
      }),
    ])
    await store.selectConversation(1)

    // 第一次失败 → refresh 成功
    mockRefreshAssetURL.mockResolvedValueOnce({ url: refreshedURL })
    await store.handleImageLoadError(10)
    expect(mockRefreshAssetURL).toHaveBeenCalledTimes(1)

    // 第二次失败（refresh 后的新 URL 仍失败）→ 不再 refresh
    const refreshed2 = await store.handleImageLoadError(10)
    expect(refreshed2).toBe(false)
    expect(mockRefreshAssetURL).toHaveBeenCalledTimes(1) // 仍只调用 1 次
  })

  it('场景5d：refresh 失败时返回 false 且标记已重试', async () => {
    const store = useImageGenerationStore()
    const originalURL = makeLocalURL('/api/media/x.png', futureExpires(600))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: originalURL })],
      }),
    ])
    await store.selectConversation(1)

    // refresh 接口失败
    mockRefreshAssetURL.mockRejectedValueOnce(new Error('network error'))
    const refreshed = await store.handleImageLoadError(10)

    expect(refreshed).toBe(false)
    expect(mockRefreshAssetURL).toHaveBeenCalledTimes(1)
    // 仍标记为已重试，避免连续重试
    expect(wasAssetRetried(10)).toBe(true)
  })

  it('场景5e：并发触发同一 asset 的 error 只 refresh 一次（Promise 去重）', async () => {
    const store = useImageGenerationStore()
    const originalURL = makeLocalURL('/api/media/x.png', futureExpires(600))
    const refreshedURL = makeLocalURL('/api/media/x.png', futureExpires(900))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url: originalURL })],
      }),
    ])
    await store.selectConversation(1)

    // 让 refresh mock hang 住，模拟慢请求
    let resolveRefresh: (v: { url: string }) => void = () => {}
    mockRefreshAssetURL.mockImplementationOnce(
      () => new Promise((resolve) => { resolveRefresh = resolve })
    )

    // 并发触发两次 handleImageLoadError（不立即 await）
    const p1 = store.handleImageLoadError(10)
    // 让微任务跑一次，让第一次调用的 markAssetRetried 生效
    await Promise.resolve()
    const p2 = store.handleImageLoadError(10)

    // 释放 refresh，让 p1 完成
    resolveRefresh({ url: refreshedURL })

    const [r1, r2] = await Promise.all([p1, p2])

    // 第一次触发 refresh，第二次因 wasAssetRetried 已标记而跳过
    expect(r1).toBe(true)
    expect(r2).toBe(false)
    expect(mockRefreshAssetURL).toHaveBeenCalledTimes(1)
  })

  // --- 场景 6：不同 asset 不会错误复用图片 ---

  it('场景6：不同 assetId 的 asset 即使 path 相同也使用独立缓存条目', async () => {
    const store = useImageGenerationStore()
    const path = '/api/media/images/same.png'
    const url1 = makeLocalURL(path, futureExpires(600))
    const url2 = makeLocalURL(path, futureExpires(700))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [
          makeAsset({ id: 10, url: url1 }),
          makeAsset({ id: 11, url: url2 }),
        ],
      }),
    ])
    await store.selectConversation(1)

    // 两个 asset 各自独立缓存
    expect(__getCacheSizeForTest()).toBe(2)
    expect(store.generations[0].output_assets![0].url).toBe(url1)
    expect(store.generations[0].output_assets![1].url).toBe(url2)
  })

  it('场景6 补充：同 assetId 不同 objectKey 视为不同 asset', async () => {
    const store = useImageGenerationStore()
    const url1 = makeLocalURL('/api/media/a.png', futureExpires(600))
    const url2 = makeLocalURL('/api/media/b.png', futureExpires(700))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [
          makeAsset({ id: 10, url: url1 }),
          makeAsset({ id: 10, url: url2 }), // 同 id 不同 path（极端场景）
        ],
      }),
    ])
    await store.selectConversation(1)

    // 两个缓存条目（assetId + objectKey 不同）
    expect(__getCacheSizeForTest()).toBe(2)
  })

  // --- 场景 7：删除会话或资源后清除对应缓存 ---

  it('场景7a：deleteGeneration 失效该 generation 下所有 asset 的缓存', async () => {
    const store = useImageGenerationStore()
    const url1 = makeLocalURL('/api/media/a.png', futureExpires(600))
    const url2 = makeLocalURL('/api/media/b.png', futureExpires(600))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        input_assets: [makeAsset({ id: 10, url: url1 })],
        output_assets: [makeAsset({ id: 11, url: url2 })],
      }),
    ])
    await store.selectConversation(1)
    expect(__getCacheSizeForTest()).toBe(2)

    // 删除 generation
    mockDeleteGeneration.mockResolvedValueOnce(undefined)
    await store.deleteGeneration(1)

    // 缓存应被清除
    expect(__getCacheSizeForTest()).toBe(0)
    expect(getCachedURL(makeAsset({ id: 10, url: url1 }))).toBeUndefined()
    expect(getCachedURL(makeAsset({ id: 11, url: url2 }))).toBeUndefined()
    // generations 列表也应移除
    expect(store.generations).toHaveLength(0)
  })

  it('场景7b：deleteConversation 失效该会话下所有 asset 的缓存', async () => {
    const store = useImageGenerationStore()
    const url1 = makeLocalURL('/api/media/a.png', futureExpires(600))
    const url2 = makeLocalURL('/api/media/b.png', futureExpires(600))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        input_assets: [makeAsset({ id: 10, url: url1 })],
        output_assets: [makeAsset({ id: 11, url: url2 })],
      }),
      makeGen({
        id: 2,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 12, url: makeLocalURL('/api/media/c.png', futureExpires(600)) })],
      }),
    ])
    await store.selectConversation(1)
    expect(__getCacheSizeForTest()).toBe(3)

    // 删除当前会话
    mockDeleteConversation.mockResolvedValueOnce(undefined)
    await store.deleteConversation(1)

    // 当前会话下所有 asset 缓存应被清除
    expect(__getCacheSizeForTest()).toBe(0)
    // currentConversation 应清空
    expect(store.currentConversation).toBeNull()
    // generations 列表应清空
    expect(store.generations).toHaveLength(0)
  })

  it('场景7c：reset 清空所有缓存（用户登出）', async () => {
    const store = useImageGenerationStore()
    const url = makeLocalURL('/api/media/a.png', futureExpires(600))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url })],
      }),
    ])
    await store.selectConversation(1)
    expect(__getCacheSizeForTest()).toBe(1)

    // 用户登出 → reset
    store.reset()

    expect(__getCacheSizeForTest()).toBe(0)
    expect(store.generations).toHaveLength(0)
    expect(store.currentConversation).toBeNull()
  })

  it('场景7d：removePendingInputAsset 失效对应 asset 的缓存', async () => {
    const store = useImageGenerationStore()
    const url = makeLocalURL('/api/media/upload.png', futureExpires(600))

    // 直接构造一个 pendingInputAsset 并写入缓存
    const asset = makeAsset({ id: 20, url })
    // 通过 store 内部方法写入缓存（模拟 uploadInputAsset 完成后的状态）
    store.pendingInputAssets = [asset]
    // 手动触发缓存写入（因为 uploadInputAsset 没有被调用）
    const { setCachedURL } = await import('@/stores/imageAssetCache')
    setCachedURL(asset, url)
    expect(__getCacheSizeForTest()).toBe(1)

    // 移除 pendingInputAsset
    store.removePendingInputAsset(20)

    expect(__getCacheSizeForTest()).toBe(0)
    expect(store.pendingInputAssets).toHaveLength(0)
  })

  it('场景7e：deleteGeneration 同时清除该 asset 的重试标记', async () => {
    const store = useImageGenerationStore()
    const url = makeLocalURL('/api/media/a.png', futureExpires(600))

    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockListGenerationsByConversation.mockResolvedValueOnce([
      makeGen({
        id: 1,
        status: 'succeeded',
        output_assets: [makeAsset({ id: 10, url })],
      }),
    ])
    await store.selectConversation(1)

    // 触发一次重试
    mockRefreshAssetURL.mockResolvedValueOnce({ url: makeLocalURL('/api/media/a.png', futureExpires(900)) })
    await store.handleImageLoadError(10)
    expect(wasAssetRetried(10)).toBe(true)

    // 删除 generation → 重试标记应清除
    mockDeleteGeneration.mockResolvedValueOnce(undefined)
    await store.deleteGeneration(1)

    expect(wasAssetRetried(10)).toBe(false)
  })
})
