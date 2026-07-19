/**
 * Image generation polling utilities
 *
 * 提供轮询控制器所需的纯函数与常量，便于单元测试覆盖。
 * 与 store 解耦，store 内部通过这些函数判断状态、计算退避时长。
 */

import type { ImageGenerationStatus } from '@/types'

// ============================================================================
// 状态分类
// ============================================================================

/**
 * 运行态：queued / pending / processing
 *
 * 这三种状态视为"任务正在进行中"，UI 应显示 loading 反馈，store 应继续轮询。
 */
export function isRunningStatus(status: ImageGenerationStatus | string | null | undefined): boolean {
  return status === 'queued' || status === 'pending' || status === 'processing'
}

/**
 * 终态：succeeded / completed / failed / canceled / timeout
 *
 * 到达终态后必须立即停止轮询。
 * `completed` 不在后端 image_generation enum 中（仅 OpenAI Responses API 使用），
 * 作为 succeeded 的别名兼容处理。
 * `timeout` 是前端虚拟状态：当轮询超时或连续失败达到阈值时由 store 写入。
 */
export function isTerminalStatus(status: ImageGenerationStatus | string | null | undefined): boolean {
  return (
    status === 'succeeded' ||
    status === 'completed' ||
    status === 'failed' ||
    status === 'canceled' ||
    status === 'timeout'
  )
}

// ============================================================================
// 轮询配置常量
// ============================================================================

/**
 * 单次轮询请求超时时间（毫秒）。
 * 超过此时间本次请求失败，进入退避重试，但不会无限挂起。
 */
export const POLL_REQUEST_TIMEOUT_MS = 15_000

/**
 * 整体轮询最大时长（毫秒）。
 * 任务从开始轮询起超过此时长仍处于运行态时，store 将其标记为 'timeout' 虚拟状态。
 *
 * 根据提示词复杂度、图像尺寸和服务器负载，图像生成可能需要数秒到几十秒。
 * 后端实际生成最长可能持续到分钟级，因此整体上限设为 360s（6 分钟），
 * 覆盖绝大部分正常生成场景，仅在任务真正卡死时才进入 timeout。
 */
export const POLL_MAX_DURATION_MS = 360 * 1000

/**
 * 轮询最小间隔（毫秒）。首次轮询与成功后的下次轮询至少等待此时长。
 */
export const POLL_INTERVAL_MIN_MS = 1_000

/**
 * 轮询最大间隔（毫秒）。指数退避封顶，避免长时间无请求。
 */
export const POLL_INTERVAL_MAX_MS = 5_000

/**
 * 连续失败次数达到此阈值后，停止轮询并将任务标记为 'timeout'。
 * 用户期望：第一次失败 2s，第二次 4s，第三次 8s，达到上限后停止。
 */
export const POLL_MAX_FAILURES = 3

// ============================================================================
// 退避计算
// ============================================================================

/**
 * 计算下一次轮询的延迟时间（毫秒）。
 *
 * 成功时按指数退避 1s → 2s → 4s → 5s（封顶），用于拉长低频检查间隔。
 * 失败时按用户期望的 2s → 4s → 8s 退避，封顶 8s。
 *
 * @param currentDelay 当前已使用的延迟
 * @param successRound 是否为成功后的退避（true 用 1s 起步；false 用 2s 起步）
 * @returns 下次延迟（毫秒）
 */
export function nextPollDelay(currentDelay: number, successRound: boolean): number {
  const base = successRound ? POLL_INTERVAL_MIN_MS : 2_000
  const cap = successRound ? POLL_INTERVAL_MAX_MS : 8_000
  // 首次调用 currentDelay=0 时直接返回 base
  if (currentDelay <= 0) return base
  const next = currentDelay * 2
  return Math.min(next, cap)
}

/**
 * 判断轮询是否已超过整体最大时长。
 *
 * @param startedAt 轮询开始时间戳（Date.now()）
 * @param now 当前时间戳
 * @returns true 表示已超时
 */
export function isPollDurationExceeded(startedAt: number, now: number): boolean {
  return now - startedAt >= POLL_MAX_DURATION_MS
}
