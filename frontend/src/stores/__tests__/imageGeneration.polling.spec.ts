import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

import type { ImageGeneration } from '@/types'
import {
  useImageGenerationStore,
} from '@/stores/imageGeneration'
import {
  POLL_MAX_DURATION_MS,
  POLL_MAX_FAILURES,
  POLL_REQUEST_TIMEOUT_MS,
} from '@/stores/imagePolling'

// ============================================================================
// Mock imageGeneration API
//
// 重点 mock getGeneration：每次调用都会推进 mockGetGenerationImplementation，
// 让测试可以模拟"任务持续 queued"、"连续失败"、"到达终态"等场景。
// ============================================================================

const mockCreateGeneration = vi.fn()
const mockGetConversation = vi.fn()
const mockGetGeneration = vi.fn()

vi.mock('@/api/imageGeneration', () => ({
  default: {
    createGeneration: (...args: any[]) => mockCreateGeneration(...args),
    getConversation: (...args: any[]) => mockGetConversation(...args),
    getGeneration: (...args: any[]) => mockGetGeneration(...args),
    listConversations: vi.fn().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 }),
    listGenerationsByConversation: vi.fn().mockResolvedValue([]),
  },
}))

// ============================================================================
// 测试辅助
// ============================================================================

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

/**
 * 在 vitest 中使用 fake timers，可以让 setTimeout 立即触发并验证调度行为。
 * 注意：fake timers 会冻结 Date.now()，需要用 vi.setSystemTime 推进时间。
 */
describe('useImageGenerationStore — 轮询控制器行为', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-18T00:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // --- 场景 1：queued 状态会启动轮询 ---
  //
  // 验证：schedulePoll(queuedGenId) 后 activeGenerationIds 包含该 id，
  //      且会调用 getGeneration API。

  it('场景1：queued 状态会启动轮询，调用 getGeneration', async () => {
    const store = useImageGenerationStore()
    const gen = makeGen({ id: 100, status: 'queued' })
    store.generations = [gen]
    // getGeneration 永远返回 succeeded（终态），避免无限轮询
    mockGetGeneration.mockResolvedValue(makeGen({ id: 100, status: 'succeeded' }))

    store.schedulePoll(100)
    expect(store.activeGenerationIds.has(100)).toBe(true)

    // 推进 1s 触发首次轮询
    await vi.advanceTimersByTimeAsync(1000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)
    // succeeded 是终态，停止轮询
    expect(store.activeGenerationIds.has(100)).toBe(false)
  })

  // --- 场景 2：processing 状态也会启动轮询 ---

  it('场景2：processing 状态会启动轮询', async () => {
    const store = useImageGenerationStore()
    const gen = makeGen({ id: 101, status: 'processing' })
    store.generations = [gen]
    mockGetGeneration.mockResolvedValue(makeGen({ id: 101, status: 'succeeded' }))

    store.schedulePoll(101)
    await vi.advanceTimersByTimeAsync(1000)

    expect(mockGetGeneration).toHaveBeenCalledWith(
      101,
      expect.objectContaining({
        signal: expect.any(AbortSignal),
        timeout: POLL_REQUEST_TIMEOUT_MS,
      })
    )
  })

  // --- 场景 3：succeeded / failed / canceled 到达终态后停止轮询 ---

  it('场景3a：succeeded 后停止轮询，不再调用 getGeneration', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 1, status: 'queued' })]
    mockGetGeneration.mockResolvedValue(makeGen({ id: 1, status: 'succeeded' }))

    store.schedulePoll(1)
    await vi.advanceTimersByTimeAsync(1000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)

    // 再推进时间，应不再调用
    await vi.advanceTimersByTimeAsync(5000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)
    expect(store.activeGenerationIds.has(1)).toBe(false)
  })

  it('场景3b：failed 后停止轮询', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 2, status: 'queued' })]
    mockGetGeneration.mockResolvedValue(makeGen({ id: 2, status: 'failed', error_code: 'IMAGE_PROVIDER_ERROR' }))

    store.schedulePoll(2)
    await vi.advanceTimersByTimeAsync(1000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(10000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)
    expect(store.activeGenerationIds.has(2)).toBe(false)
  })

  it('场景3c：canceled 后停止轮询', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 3, status: 'queued' })]
    mockGetGeneration.mockResolvedValue(makeGen({ id: 3, status: 'canceled' }))

    store.schedulePoll(3)
    await vi.advanceTimersByTimeAsync(1000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(10000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)
    expect(store.activeGenerationIds.has(3)).toBe(false)
  })

  // --- 场景 4：queued 持续超过最大等待时间后停止轮询并进入 timeout ---

  it('场景4：queued 持续超过 POLL_MAX_DURATION_MS 后标记为 timeout', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 4, status: 'queued' })]
    // getGeneration 永远返回 queued（任务卡死）
    mockGetGeneration.mockResolvedValue(makeGen({ id: 4, status: 'queued' }))

    store.schedulePoll(4)
    // 推进时间超过 6 分钟（POLL_MAX_DURATION_MS = 360s）
    // 由于 fake timer 下 Date.now() 不会自动推进，需手动设置
    vi.setSystemTime(new Date('2026-07-18T00:06:01Z')) // 6 分 1 秒后
    // 触发下一次轮询调度（任意 advanceTimersByTime 都会触发 scheduleNextPoll 的超时检查）
    await vi.advanceTimersByTimeAsync(2000)

    // 任务被标记为 timeout
    const gen = store.generations.find((g) => g.id === 4)
    expect(gen?.status).toBe('timeout')
    expect(store.activeGenerationIds.has(4)).toBe(false)
  })

  // --- 场景 5：processing 持续超过最大等待时间后标记为 timeout ---

  it('场景5：processing 持续超过 POLL_MAX_DURATION_MS 后标记为 timeout', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 5, status: 'processing' })]
    mockGetGeneration.mockResolvedValue(makeGen({ id: 5, status: 'processing' }))

    store.schedulePoll(5)
    vi.setSystemTime(new Date('2026-07-18T00:06:01Z'))
    await vi.advanceTimersByTimeAsync(2000)

    const gen = store.generations.find((g) => g.id === 5)
    expect(gen?.status).toBe('timeout')
    expect(store.activeGenerationIds.has(5)).toBe(false)
  })

  // --- 场景 6：连续请求失败达到阈值后停止轮询 ---

  it('场景6：连续失败 POLL_MAX_FAILURES 次后标记为 timeout', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 6, status: 'queued' })]
    // getGeneration 永远 reject（网络错误）
    mockGetGeneration.mockRejectedValue({ status: 0, message: 'Network error' })

    store.schedulePoll(6)

    // 第一次失败（2s 退避）
    await vi.advanceTimersByTimeAsync(1000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)
    // 推进退避时间触发第二次失败
    await vi.advanceTimersByTimeAsync(2000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(2)
    // 推进退避时间触发第三次失败（达到上限）
    await vi.advanceTimersByTimeAsync(4000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(3)

    // 第三次失败后达到 POLL_MAX_FAILURES，标记 timeout
    const gen = store.generations.find((g) => g.id === 6)
    expect(gen?.status).toBe('timeout')
    expect(store.activeGenerationIds.has(6)).toBe(false)

    // 不应再调用 getGeneration
    await vi.advanceTimersByTimeAsync(10000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(POLL_MAX_FAILURES)
  })

  // --- 场景 7：同一 generationId 重复调用 schedulePoll 不会创建多个轮询器 ---

  it('场景7：同一 generationId 重复调用 schedulePoll 不会创建多个轮询器', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 7, status: 'queued' })]
    // 让请求 hang 住，模拟 inFlight 状态
    let resolveApi: (v: ImageGeneration) => void = () => {}
    mockGetGeneration.mockImplementation(
      () =>
        new Promise<ImageGeneration>((resolve) => {
          resolveApi = resolve
        })
    )

    store.schedulePoll(7)
    // 触发首次请求
    await vi.advanceTimersByTimeAsync(1000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)

    // 此时 inFlight=true，重复调用 schedulePoll 应被忽略
    store.schedulePoll(7)
    store.schedulePoll(7)

    // 没有新请求发出
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)

    // 释放请求，让上下文清理
    resolveApi(makeGen({ id: 7, status: 'succeeded' }))
    await vi.advanceTimersByTimeAsync(0)
  })

  it('场景7b：timer 已调度但未触发时，重复 schedulePoll 也不会创建第二个 timer', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 71, status: 'queued' })]
    mockGetGeneration.mockResolvedValue(makeGen({ id: 71, status: 'succeeded' }))

    store.schedulePoll(71)
    // 此时 timer 已调度但未触发（inFlight=false）
    store.schedulePoll(71)
    store.schedulePoll(71)

    // 触发后只调用一次
    await vi.advanceTimersByTimeAsync(1000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)
  })

  // --- 场景 8：切换会话 / stopAllPolling 时清理轮询器和未完成请求 ---

  it('场景8a：stopAllPolling 清理所有活跃轮询器', async () => {
    const store = useImageGenerationStore()
    store.generations = [
      makeGen({ id: 80, status: 'queued' }),
      makeGen({ id: 81, status: 'processing' }),
    ]
    let resolve80: (v: ImageGeneration) => void = () => {}
    let resolve81: (v: ImageGeneration) => void = () => {}
    mockGetGeneration.mockImplementation((id: number) =>
      new Promise<ImageGeneration>((resolve) => {
        if (id === 80) resolve80 = resolve
        else resolve81 = resolve
      })
    )

    store.schedulePoll(80)
    store.schedulePoll(81)
    await vi.advanceTimersByTimeAsync(1000)

    expect(store.activeGenerationIds.size).toBe(2)

    // 调用 stopAllPolling：应清空所有 activeGenerationIds
    store.stopAllPolling()
    expect(store.activeGenerationIds.size).toBe(0)

    // 释放请求，由于 ctx.stopped=true，回调不会再 upsertGeneration 或调度下次
    resolve80(makeGen({ id: 80, status: 'succeeded' }))
    resolve81(makeGen({ id: 81, status: 'succeeded' }))
    await vi.advanceTimersByTimeAsync(5000)

    // 由于 stopped，请求完成时不会触发新一轮调度
    expect(mockGetGeneration).toHaveBeenCalledTimes(2)
  })

  it('场景8b：stopPoll 单独停止指定 generationId 的轮询', async () => {
    const store = useImageGenerationStore()
    store.generations = [
      makeGen({ id: 82, status: 'queued' }),
      makeGen({ id: 83, status: 'queued' }),
    ]
    mockGetGeneration.mockResolvedValue(makeGen({ id: 999, status: 'queued' }))

    store.schedulePoll(82)
    store.schedulePoll(83)
    await vi.advanceTimersByTimeAsync(1000)

    // 停止 82，保留 83
    store.stopPoll(82)
    expect(store.activeGenerationIds.has(82)).toBe(false)
    expect(store.activeGenerationIds.has(83)).toBe(true)
  })

  // --- 补充场景：abort 在飞请求 ---

  it('补充：stopAllPolling 会 abort 在飞的请求（不视为失败，不标记 timeout）', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 9, status: 'queued' })]

    // 用 abort 信号验证：mock 实现检查 signal 是否被 abort
    mockGetGeneration.mockImplementation(
      (_id: number, options?: { signal?: AbortSignal }) =>
        new Promise<ImageGeneration>((_resolve, reject) => {
          const signal = options?.signal
          if (signal) {
            if (signal.aborted) {
              reject(Object.assign(new Error('canceled'), { code: 'ERR_CANCELED' }))
            } else {
              signal.addEventListener('abort', () => {
                reject(Object.assign(new Error('canceled'), { code: 'ERR_CANCELED' }))
              })
            }
          }
        })
    )

    store.schedulePoll(9)
    await vi.advanceTimersByTimeAsync(1000)
    // 请求在飞中
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)

    // 停止轮询：应 abort 请求
    store.stopAllPolling()
    await vi.advanceTimersByTimeAsync(0)

    // 任务状态保持 queued（未被标记为 timeout，因为 abort 不计入 failureCount）
    const gen = store.generations.find((g) => g.id === 9)
    expect(gen?.status).toBe('queued')
    expect(store.activeGenerationIds.has(9)).toBe(false)
  })

  // --- 补充场景：成功后退避调度下次 ---

  it('补充：成功返回 queued 后按指数退避调度下次轮询', async () => {
    const store = useImageGenerationStore()
    store.generations = [makeGen({ id: 10, status: 'queued' })]
    // 第一次返回 queued（继续轮询），第二次返回 succeeded（终态）
    mockGetGeneration
      .mockResolvedValueOnce(makeGen({ id: 10, status: 'queued' }))
      .mockResolvedValueOnce(makeGen({ id: 10, status: 'succeeded' }))

    store.schedulePoll(10)

    // 首次 1s 触发
    await vi.advanceTimersByTimeAsync(1000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(1)

    // 退避 2s 后触发第二次（1s→2s）
    await vi.advanceTimersByTimeAsync(2000)
    expect(mockGetGeneration).toHaveBeenCalledTimes(2)

    // succeeded 终态，停止
    expect(store.activeGenerationIds.has(10)).toBe(false)
  })

  // --- 补充场景：创建任务时立即启动轮询 ---

  it('补充：createGeneration 返回 queued 后立即启动轮询', async () => {
    const store = useImageGenerationStore()
    const createdGen = makeGen({ id: 11, status: 'queued' })
    mockCreateGeneration.mockResolvedValue(createdGen)
    mockGetConversation.mockResolvedValue({ id: 1, title: 'test' })
    mockGetGeneration.mockResolvedValue(makeGen({ id: 11, status: 'succeeded' }))

    await store.createGeneration({
      type: 'text_to_image',
      prompt: 'apple',
      size: '2K',
      ratio: '1:1',
    })

    expect(store.activeGenerationIds.has(11)).toBe(true)

    // 推进时间触发轮询，最终到达终态停止
    await vi.advanceTimersByTimeAsync(1000)
    expect(store.activeGenerationIds.has(11)).toBe(false)
  })
})
