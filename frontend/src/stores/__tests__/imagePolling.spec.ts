import { describe, it, expect } from 'vitest'

import {
  isRunningStatus,
  isTerminalStatus,
  nextPollDelay,
  isPollDurationExceeded,
  POLL_REQUEST_TIMEOUT_MS,
  POLL_MAX_DURATION_MS,
  POLL_INTERVAL_MIN_MS,
  POLL_INTERVAL_MAX_MS,
  POLL_MAX_FAILURES,
} from '../imagePolling'

// ============================================================================
// 测试目标：
//   1. isRunningStatus 正确识别 queued/pending/processing
//   2. isTerminalStatus 正确识别 succeeded/completed/failed/canceled/timeout
//   3. nextPollDelay 成功/失败退避计算正确
//   4. isPollDurationExceeded 超时判断正确
//   5. 常量值符合设计预期（15s 请求超时、6 分钟整体超时、3 次失败上限）
// ============================================================================

describe('isRunningStatus', () => {
  it('queued / pending / processing 返回 true', () => {
    expect(isRunningStatus('queued')).toBe(true)
    expect(isRunningStatus('pending')).toBe(true)
    expect(isRunningStatus('processing')).toBe(true)
  })

  it('succeeded / failed / canceled / timeout 返回 false', () => {
    expect(isRunningStatus('succeeded')).toBe(false)
    expect(isRunningStatus('failed')).toBe(false)
    expect(isRunningStatus('canceled')).toBe(false)
    expect(isRunningStatus('timeout')).toBe(false)
  })

  it('completed 别名返回 false（视为终态）', () => {
    expect(isRunningStatus('completed')).toBe(false)
  })

  it('未知状态返回 false', () => {
    expect(isRunningStatus('unknown')).toBe(false)
    expect(isRunningStatus('')).toBe(false)
    expect(isRunningStatus(null)).toBe(false)
    expect(isRunningStatus(undefined)).toBe(false)
  })
})

describe('isTerminalStatus', () => {
  it('succeeded / completed / failed / canceled / timeout 返回 true', () => {
    expect(isTerminalStatus('succeeded')).toBe(true)
    expect(isTerminalStatus('completed')).toBe(true)
    expect(isTerminalStatus('failed')).toBe(true)
    expect(isTerminalStatus('canceled')).toBe(true)
    expect(isTerminalStatus('timeout')).toBe(true)
  })

  it('queued / pending / processing 返回 false', () => {
    expect(isTerminalStatus('queued')).toBe(false)
    expect(isTerminalStatus('pending')).toBe(false)
    expect(isTerminalStatus('processing')).toBe(false)
  })

  it('未知状态返回 false', () => {
    expect(isTerminalStatus('unknown')).toBe(false)
    expect(isTerminalStatus('')).toBe(false)
    expect(isTerminalStatus(null)).toBe(false)
    expect(isTerminalStatus(undefined)).toBe(false)
  })
})

describe('nextPollDelay — 成功退避（1s→2s→5s 封顶）', () => {
  it('首次调用 currentDelay=0 返回基础值 1s', () => {
    expect(nextPollDelay(0, true)).toBe(POLL_INTERVAL_MIN_MS) // 1000
  })

  it('1s → 2s', () => {
    expect(nextPollDelay(1000, true)).toBe(2000)
  })

  it('2s → 4s', () => {
    expect(nextPollDelay(2000, true)).toBe(4000)
  })

  it('4s → 5s（封顶）', () => {
    expect(nextPollDelay(4000, true)).toBe(POLL_INTERVAL_MAX_MS) // 5000
  })

  it('5s → 5s（封顶，不再增加）', () => {
    expect(nextPollDelay(5000, true)).toBe(POLL_INTERVAL_MAX_MS)
  })
})

describe('nextPollDelay — 失败退避（2s→4s→8s 封顶）', () => {
  it('首次调用 currentDelay=0 返回失败基础值 2s', () => {
    expect(nextPollDelay(0, false)).toBe(2000)
  })

  it('2s → 4s', () => {
    expect(nextPollDelay(2000, false)).toBe(4000)
  })

  it('4s → 8s', () => {
    expect(nextPollDelay(4000, false)).toBe(8000)
  })

  it('8s → 8s（封顶，不再增加）', () => {
    expect(nextPollDelay(8000, false)).toBe(8000)
  })
})

describe('isPollDurationExceeded', () => {
  it('刚启动时未超时', () => {
    const now = Date.now()
    expect(isPollDurationExceeded(now, now)).toBe(false)
    expect(isPollDurationExceeded(now, now + 1000)).toBe(false)
  })

  it('超过 6 分钟后视为超时', () => {
    const startedAt = Date.now()
    const after6min = startedAt + POLL_MAX_DURATION_MS
    expect(isPollDurationExceeded(startedAt, after6min)).toBe(true)
    expect(isPollDurationExceeded(startedAt, after6min + 1)).toBe(true)
  })

  it('5分59秒仍视为未超时', () => {
    const startedAt = Date.now()
    const justBefore = startedAt + POLL_MAX_DURATION_MS - 1
    expect(isPollDurationExceeded(startedAt, justBefore)).toBe(false)
  })
})

describe('轮询配置常量', () => {
  it('POLL_REQUEST_TIMEOUT_MS = 15s（用户期望 10s/15s）', () => {
    expect(POLL_REQUEST_TIMEOUT_MS).toBe(15_000)
  })

  it('POLL_MAX_DURATION_MS = 6 分钟（360 秒，覆盖大部分正常生成场景）', () => {
    expect(POLL_MAX_DURATION_MS).toBe(360 * 1000)
  })

  it('POLL_INTERVAL_MIN_MS = 1s', () => {
    expect(POLL_INTERVAL_MIN_MS).toBe(1000)
  })

  it('POLL_INTERVAL_MAX_MS = 5s', () => {
    expect(POLL_INTERVAL_MAX_MS).toBe(5000)
  })

  it('POLL_MAX_FAILURES = 3（用户期望 3 次失败后停止）', () => {
    expect(POLL_MAX_FAILURES).toBe(3)
  })
})
