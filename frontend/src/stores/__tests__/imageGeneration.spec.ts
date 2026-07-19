import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

import type { ImageAsset, ImageGeneration } from '@/types'
import {
  extractObjectKey,
  assetStableKey,
  isSignedURLExpiringSoon,
  mergeAssetURLs,
  useImageGenerationStore,
} from '@/stores/imageGeneration'

// ============================================================================
// Mock imageGeneration API
// ============================================================================

const mockCreateGeneration = vi.fn()
const mockGetConversation = vi.fn()

vi.mock('@/api/imageGeneration', () => ({
  default: {
    createGeneration: (...args: any[]) => mockCreateGeneration(...args),
    getConversation: (...args: any[]) => mockGetConversation(...args),
    listConversations: vi.fn().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 }),
    listGenerationsByConversation: vi.fn().mockResolvedValue([]),
  },
}))

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

// ============================================================================
// createGeneration store action — 会话级单轮约束（IMAGE_TASK_ALREADY_RUNNING）
//
// 覆盖用户提出的 6 个场景中的前端部分：
//   1. 空会话首次提交成功
//   2. 已有 processing 任务时再次提交被前端拦截（不发起请求）
//   3. 已有 succeeded 任务时再次提交被前端拦截（不发起请求）
//   4. 快速双击只创建一个任务（creatingGeneration 标志位）
//   5. 两个并发请求只能有一个到达 API（creatingGeneration 标志位）
//   6. 后端返回 409 时：不新增生成卡片、不启动轮询
// failed/canceled 状态允许在同一会话重试（补充场景）
// ============================================================================

/** 构造一个最小可用的 ImageGeneration */
function makeGen(overrides: Partial<ImageGeneration> = {}): ImageGeneration {
  return {
    id: 1,
    conversation_id: 1,
    provider: 'agnes',
    generation_type: 'text_to_image',
    prompt: 'test',
    size: '2K',
    ratio: '1:1',
    status: 'queued',
    duration_ms: 0,
    created_at: '2026-07-18T00:00:00Z',
    updated_at: '2026-07-18T00:00:00Z',
    ...overrides,
  }
}

/** 构造 409 错误（与 axios 拦截器抛出的错误形状一致） */
function make409Error() {
  return {
    status: 409,
    code: 'IMAGE_TASK_ALREADY_RUNNING',
    reason: 'IMAGE_TASK_ALREADY_RUNNING',
    message: 'a generation task already exists in this conversation',
  }
}

describe('useImageGenerationStore — createGeneration 会话级单轮约束', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // --- 场景 1：空会话首次提交成功 ---

  it('场景1：空会话首次提交成功，调用 API 并 upsert generation', async () => {
    const store = useImageGenerationStore()
    const createdGen = makeGen({ id: 100, status: 'queued' })
    mockCreateGeneration.mockResolvedValue(createdGen)
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })

    // draft 状态：currentConversation 为 null
    const gen = await store.createGeneration({
      type: 'text_to_image',
      prompt: 'apple',
      size: '2K',
      ratio: '1:1',
    })

    expect(gen).toEqual(createdGen)
    expect(mockCreateGeneration).toHaveBeenCalledTimes(1)
    expect(store.generations).toHaveLength(1)
    expect(store.generations[0].id).toBe(100)
    // queued 状态应启动轮询
    expect(store.hasActiveGeneration).toBe(true)
  })

  // --- 场景 2：已有 processing 任务时再次提交被前端拦截 ---

  it('场景2：已有 processing 任务时再次提交被前端拦截，不发起请求', async () => {
    const store = useImageGenerationStore()
    // 模拟选中会话且已有 processing 任务
    store.currentConversation = { id: 1, title: 'apple' } as any
    store.generations = [makeGen({ id: 1, status: 'processing' })]

    const err = await store
      .createGeneration({ type: 'text_to_image', prompt: 'banana', size: '2K', ratio: '1:1' })
      .catch((e) => e)

    expect(err).toBeDefined()
    expect(err.status).toBe(409)
    expect(err.reason).toBe('IMAGE_TASK_ALREADY_RUNNING')
    // API 不应被调用
    expect(mockCreateGeneration).not.toHaveBeenCalled()
    // generations 列表不变（不新增卡片）
    expect(store.generations).toHaveLength(1)
    expect(store.generations[0].id).toBe(1)
  })

  // --- 场景 3：已有 succeeded 任务时再次提交被前端拦截 ---

  it('场景3：已有 succeeded 任务时再次提交被前端拦截，不发起请求', async () => {
    const store = useImageGenerationStore()
    store.currentConversation = { id: 1, title: 'apple' } as any
    store.generations = [makeGen({ id: 1, status: 'succeeded' })]

    const err = await store
      .createGeneration({ type: 'text_to_image', prompt: 'banana', size: '2K', ratio: '1:1' })
      .catch((e) => e)

    expect(err).toBeDefined()
    expect(err.status).toBe(409)
    expect(err.reason).toBe('IMAGE_TASK_ALREADY_RUNNING')
    expect(mockCreateGeneration).not.toHaveBeenCalled()
    expect(store.generations).toHaveLength(1)
  })

  // --- 场景 4：快速双击只创建一个任务（creatingGeneration 标志位） ---

  it('场景4：快速双击只创建一个任务，第二次直接抛 409', async () => {
    const store = useImageGenerationStore()
    const createdGen = makeGen({ id: 100, status: 'queued' })

    // 让 API 调用 hang 住，模拟请求未返回时第二次调用进来
    let resolveApi: (v: ImageGeneration) => void = () => {}
    mockCreateGeneration.mockImplementation(
      () =>
        new Promise<ImageGeneration>((resolve) => {
          resolveApi = resolve
        })
    )
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })

    // 第一次调用：发起请求，进入 pending 状态
    const p1 = store.createGeneration({
      type: 'text_to_image',
      prompt: 'apple',
      size: '2K',
      ratio: '1:1',
    })

    // 第二次调用：第一次还没返回，应被 creatingGeneration 标志位拦截
    const err2 = await store
      .createGeneration({ type: 'text_to_image', prompt: 'banana', size: '2K', ratio: '1:1' })
      .catch((e) => e)

    expect(err2).toBeDefined()
    expect(err2.status).toBe(409)
    expect(err2.reason).toBe('IMAGE_TASK_ALREADY_RUNNING')
    // 第二次调用不应再触发 API
    expect(mockCreateGeneration).toHaveBeenCalledTimes(1)

    // 释放第一次请求
    resolveApi(createdGen)
    const gen1 = await p1
    expect(gen1.id).toBe(100)
  })

  // --- 场景 5：两个并发请求只能有一个到达 API ---

  it('场景5：两个并发请求只能有一个到达 API', async () => {
    const store = useImageGenerationStore()
    const createdGen = makeGen({ id: 100, status: 'queued' })

    let resolveApi: (v: ImageGeneration) => void = () => {}
    mockCreateGeneration.mockImplementation(
      () =>
        new Promise<ImageGeneration>((resolve) => {
          resolveApi = resolve
        })
    )
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })

    // 同时发起两个请求
    const p1 = store.createGeneration({
      type: 'text_to_image',
      prompt: 'apple',
      size: '2K',
      ratio: '1:1',
    })
    const p2 = store
      .createGeneration({
        type: 'text_to_image',
        prompt: 'banana',
        size: '2K',
        ratio: '1:1',
      })
      .catch((e) => e)

    // 等所有微任务结束
    const err2 = await p2
    expect(err2).toBeDefined()
    expect(err2.status).toBe(409)
    expect(mockCreateGeneration).toHaveBeenCalledTimes(1)

    // 释放第一个请求
    resolveApi(createdGen)
    const gen1 = await p1
    expect(gen1.id).toBe(100)
  })

  // --- 场景 6：后端返回 409 时，不新增生成卡片、不启动轮询 ---

  it('场景6：后端返回 409 时，不新增生成卡片、不启动轮询', async () => {
    const store = useImageGenerationStore()
    // 注意：此处 currentConversation 必须为 null（draft），
    // 否则会被前端预校验拦截而不会到达 API。
    // 模拟后端在事务内发现已有任务，返回 409。
    mockCreateGeneration.mockRejectedValue(make409Error())

    const err = await store
      .createGeneration({ type: 'text_to_image', prompt: 'apple', size: '2K', ratio: '1:1' })
      .catch((e) => e)

    expect(err).toBeDefined()
    expect(err.status).toBe(409)
    expect(mockCreateGeneration).toHaveBeenCalledTimes(1)
    // 关键断言：不新增生成卡片
    expect(store.generations).toHaveLength(0)
    // 关键断言：不启动轮询
    expect(store.hasActiveGeneration).toBe(false)
    expect(store.activeGenerationIds.size).toBe(0)
    // creatingGeneration 已复位（finally 块执行）
    expect(store.creatingGeneration).toBe(false)
  })

  // --- 补充场景：failed 状态允许在同一会话重试 ---

  it('补充：failed 状态允许在同一会话重试，不被前端预校验拦截', async () => {
    const store = useImageGenerationStore()
    store.currentConversation = { id: 1, title: 'apple' } as any
    // 会话已有 failed 任务（视为终态失败，允许重试）
    store.generations = [makeGen({ id: 1, status: 'failed' })]

    const createdGen = makeGen({ id: 2, status: 'queued' })
    mockCreateGeneration.mockResolvedValue(createdGen)
    mockGetConversation.mockResolvedValue({ id: 1, title: 'apple' })

    const gen = await store.createGeneration({
      type: 'text_to_image',
      prompt: 'retry',
      size: '2K',
      ratio: '1:1',
      conversation_id: 1,
    })

    expect(gen.id).toBe(2)
    expect(mockCreateGeneration).toHaveBeenCalledTimes(1)
    // generations 列表新增了一条（重试记录）
    expect(store.generations).toHaveLength(2)
  })

  // --- 补充场景：canceled 状态允许在同一会话重试 ---

  it('补充：canceled 状态允许在同一会话重试，不被前端预校验拦截', async () => {
    const store = useImageGenerationStore()
    store.currentConversation = { id: 1, title: 'apple' } as any
    store.generations = [makeGen({ id: 1, status: 'canceled' })]

    const createdGen = makeGen({ id: 2, status: 'queued' })
    mockCreateGeneration.mockResolvedValue(createdGen)
    mockGetConversation.mockResolvedValue({ id: 1, title: 'apple' })

    const gen = await store.createGeneration({
      type: 'text_to_image',
      prompt: 'retry after cancel',
      size: '2K',
      ratio: '1:1',
      conversation_id: 1,
    })

    expect(gen.id).toBe(2)
    expect(mockCreateGeneration).toHaveBeenCalledTimes(1)
    expect(store.generations).toHaveLength(2)
  })

  // --- 补充场景：hasActiveOrSucceededGeneration getter 行为 ---

  it('补充：hasActiveOrSucceededGeneration 正确识别各种状态', () => {
    const store = useImageGenerationStore()

    // 空列表 → false
    expect(store.hasActiveOrSucceededGeneration).toBe(false)

    // pending → true
    store.generations = [makeGen({ id: 1, status: 'pending' })]
    expect(store.hasActiveOrSucceededGeneration).toBe(true)

    // queued → true
    store.generations = [makeGen({ id: 1, status: 'queued' })]
    expect(store.hasActiveOrSucceededGeneration).toBe(true)

    // processing → true
    store.generations = [makeGen({ id: 1, status: 'processing' })]
    expect(store.hasActiveOrSucceededGeneration).toBe(true)

    // succeeded → true
    store.generations = [makeGen({ id: 1, status: 'succeeded' })]
    expect(store.hasActiveOrSucceededGeneration).toBe(true)

    // failed → false（允许重试）
    store.generations = [makeGen({ id: 1, status: 'failed' })]
    expect(store.hasActiveOrSucceededGeneration).toBe(false)

    // canceled → false（允许重试）
    store.generations = [makeGen({ id: 1, status: 'canceled' })]
    expect(store.hasActiveOrSucceededGeneration).toBe(false)
  })
})
