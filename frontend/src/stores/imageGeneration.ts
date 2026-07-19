/**
 * Image Generation Store
 *
 * Pinia setup store managing user-side AI image generation state:
 *  - 会话列表（带分页）
 *  - 当前会话 + 该会话下的生成任务
 *  - 资产上传 pipeline（presign → S3 PUT → confirm）
 *  - 生成任务状态轮询（pending/processing → 终态）
 *
 * 安全：
 *  - 仅持有当前用户的数据（后端按 JWT user_id 强制隔离）
 *  - 不缓存任何上游凭据信息
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import imageGenerationAPI from '@/api/imageGeneration'
import type {
  CreateImageGenerationRequest,
  ImageAsset,
  ImageConversation,
  ImageGeneration,
  ImageGenerationListParams,
  ImageGenerationStatus,
  ImageConversationListParams,
  PaginatedResponse,
} from '@/types'
import {
  isRunningStatus,
  isTerminalStatus,
  isPollDurationExceeded,
  nextPollDelay,
  POLL_REQUEST_TIMEOUT_MS,
  POLL_INTERVAL_MIN_MS,
  POLL_MAX_FAILURES,
} from './imagePolling'

// 默认分页
const DEFAULT_PAGE_SIZE = 20

/**
 * 会话级单轮约束的"阻塞状态"集合。
 * 只要当前会话存在以下状态的任务，就禁止再次提交：
 *   - pending / queued / processing：进行中
 *   - succeeded：已成功（一个会话最多一张成功图）
 * failed / canceled 视为终态失败，允许在同一会话重试。
 * 与后端 repository.CreateIfUnderUserConcurrency 的会话级检查保持一致。
 */
const CONVERSATION_BLOCKING_STATUSES: ReadonlySet<ImageGenerationStatus> = new Set([
  'pending',
  'queued',
  'processing',
  'succeeded',
])

/**
 * 生成 36 字符的 UUID v4 风格 idempotency key。
 * 用 crypto.randomUUID（现代浏览器/Node 19+），不可用时回退到 Math.random 拼装。
 */
function makeIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  // 回退：不严格的 UUID v4 拼装，仅用于幂等键，碰撞概率足够低
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (ch) => {
    const r = (Math.random() * 16) | 0
    const v = ch === 'x' ? r : (r & 0x3) | 0x8
    return v.toString(16)
  })
}

// ============================================================================
// URL 合并 / 过期判断纯函数
//
// 抽出到模块顶层以便单元测试。setup store 内部直接引用这些函数。
// ============================================================================

/**
 * 从签名 URL 中提取 objectKey（URL path 部分），解析失败返回空串。
 * 例如 `/api/media/images/xxx.png` → `/api/media/images/xxx.png`。
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
 * 构造 asset 的稳定判断键：`asset.id + objectKey`。
 * objectKey 从 URL 的 path 部分提取，同一 asset.id + 同一 objectKey 才认为是同一张图片。
 * URL 缺失或解析失败时退化为仅用 id。
 */
export function assetStableKey(a: ImageAsset): string {
  const objectKey = extractObjectKey(a.url)
  return objectKey ? `${a.id}|${objectKey}` : `${a.id}|`
}

/**
 * 检查签名 URL 是否在 bufferSeconds 内过期。
 *
 * 兼容 `expires` 和 `exp` 两种参数名（后端实现统一为 `expires`，但兼容 `exp`
 * 以防未来切换到更短的参数名时前端漏改导致始终误判为"即将过期"）。
 *
 * 解析失败（URL 无 expires/exp 参数、非法数字、相对路径、URL 解析异常等）时返回 true，
 * 使调用方使用后端返回的新 URL，避免一直保留无法判断过期的旧地址。
 */
export function isSignedURLExpiringSoon(url: string, bufferSeconds: number): boolean {
  try {
    const u = new URL(url, window.location.origin)
    const expiresStr = u.searchParams.get('expires') ?? u.searchParams.get('exp')
    if (!expiresStr) return true // 无 expires/exp 参数，按"即将过期"处理
    const expires = parseInt(expiresStr, 10)
    if (Number.isNaN(expires) || expires === 0) return true // 非法数字或非签名 URL，按"即将过期"处理
    return Date.now() / 1000 + bufferSeconds >= expires
  } catch {
    return true
  }
}

/**
 * 合并新旧 asset 列表的 URL：如果同一 asset（id + objectKey 均一致）已存在且旧 URL 尚未接近过期，
 * 保留旧 URL 以命中浏览器缓存；否则使用新 URL。
 *
 * 判断键采用 `asset.id + objectKey` 而非仅 `asset.id`，以确保 asset.id 对应的图片内容不可变：
 * 即使后端在极端场景下复用了 asset.id（如重新生成覆盖了原图片，objectKey 变化），
 * 也不会用旧图片的 URL 替换新图片，避免显示陈旧内容。
 */
export function mergeAssetURLs(
  oldAssets: ImageAsset[] | undefined,
  newAssets: ImageAsset[] | undefined
): ImageAsset[] | undefined {
  if (!newAssets || newAssets.length === 0) return newAssets
  if (!oldAssets || oldAssets.length === 0) return newAssets
  const oldMap = new Map(oldAssets.map((a) => [assetStableKey(a), a]))
  return newAssets.map((a) => {
    const old = oldMap.get(assetStableKey(a))
    if (!old || !old.url || !a.url) return a
    if (a.url === old.url) return a
    // 旧 URL 快过期（60s 内）则用新 URL 刷新
    if (isSignedURLExpiringSoon(old.url, 60)) return a
    // 旧 URL 仍有效，保留以命中浏览器缓存
    return { ...a, url: old.url }
  })
}

export const useImageGenerationStore = defineStore('imageGeneration', () => {
  // ==================== State ====================

  /** 会话列表（分页） */
  const conversations = ref<ImageConversation[]>([])
  const conversationsTotal = ref(0)
  const conversationsPage = ref(1)
  const conversationsPageSize = ref(DEFAULT_PAGE_SIZE)
  const conversationsLoading = ref(false)

  /** 当前选中的会话 */
  const currentConversation = ref<ImageConversation | null>(null)
  /** 当前会话下的生成任务 */
  const generations = ref<ImageGeneration[]>([])
  const generationsLoading = ref(false)
  /** 是否处于"新建会话"草稿态（点击新建会话后、首次提交生成任务前） */
  const isDraftConversation = ref(false)

  /** 进行中的生成任务（pending/processing），用于状态轮询 */
  const activeGenerationIds = ref<Set<number>>(new Set())

  /** 已上传但未提交给生成任务的输入资产（img2img 用） */
  const pendingInputAssets = ref<ImageAsset[]>([])

  /** 创建生成任务 loading（防止重复提交） */
  const creatingGeneration = ref(false)
  /** 上传资产 loading */
  const uploadingAsset = ref(false)

  /** 上一次错误（用于 UI 提示，不带敏感信息） */
  const lastError = ref<string | null>(null)

  // ==================== 轮询内部状态 ====================

  /**
   * 每个 generationId 对应的轮询上下文。
   *
   * 一个 generationId 同一时刻最多存在一个 PollContext：
   *   - timer 已调度但未触发 → inFlight=false
   *   - 请求已发出等待响应 → inFlight=true
   * 两种状态都属于"活跃轮询器"，schedulePoll 检测到任一存在都会直接返回。
   */
  interface PollContext {
    timer: ReturnType<typeof setTimeout> | null
    abortController: AbortController | null
    startedAt: number
    failureCount: number
    lastDelay: number
    /** 当前是否正在等待 HTTP 响应；true 时不允许再发起下一次请求 */
    inFlight: boolean
    /** 上下文是否已主动停止（避免 stop 后异步回调仍触发新调度） */
    stopped: boolean
  }
  const pollContexts = new Map<number, PollContext>()

  // ==================== Getters ====================

  const hasConversations = computed(() => conversations.value.length > 0)
  const hasActiveGeneration = computed(() => activeGenerationIds.value.size > 0)
  const totalConversationsPages = computed(() =>
    Math.max(1, Math.ceil(conversationsTotal.value / conversationsPageSize.value))
  )

  /**
   * 当前会话是否存在"有效"生图任务（pending/queued/processing/succeeded）。
   * 用于前端单轮约束：只要存在有效任务就禁止再次提交。
   * failed/canceled 视为终态失败，允许在同一会话重试。
   * 与后端 CreateIfUnderUserConcurrency 的会话级检查保持一致。
   */
  const hasActiveOrSucceededGeneration = computed(() =>
    generations.value.some((g) => CONVERSATION_BLOCKING_STATUSES.has(g.status))
  )

  // ==================== Internal Helpers ====================

  function setActive(id: number, active: boolean) {
    const next = new Set(activeGenerationIds.value)
    if (active) {
      next.add(id)
    } else {
      next.delete(id)
    }
    activeGenerationIds.value = next
  }

  /**
   * 停止并清理指定 generationId 的轮询上下文。
   *
   * 同时处理三件事：
   *   1. 标记 stopped=true，使已发出的异步回调不会重新调度下一次轮询
   *   2. abort 任何在飞的 HTTP 请求（避免请求完成时污染 store 状态）
   *   3. clear 已调度的 setTimeout（避免下次轮询触发）
   *
   * 调用方负责同步更新 activeGenerationIds（通过 setActive(id, false)）。
   */
  function clearPollTimer(id: number) {
    const ctx = pollContexts.get(id)
    if (!ctx) return
    ctx.stopped = true
    if (ctx.timer !== null) {
      clearTimeout(ctx.timer)
      ctx.timer = null
    }
    if (ctx.abortController) {
      try {
        ctx.abortController.abort()
      } catch {
        // abort 失败不影响后续逻辑
      }
      ctx.abortController = null
    }
    pollContexts.delete(id)
  }

  function setLastError(err: unknown) {
    if (err instanceof Error) {
      lastError.value = err.message
    } else if (typeof err === 'string') {
      lastError.value = err
    } else {
      lastError.value = null
    }
  }

  /** 把单个 generation 合并进 generations 列表（按 id 去重，保留顺序） */
  function upsertGeneration(gen: ImageGeneration) {
    const idx = generations.value.findIndex((g) => g.id === gen.id)
    if (idx >= 0) {
      const existing = generations.value[idx]
      // 轮询时 asset.id + objectKey 不变则保留旧 URL，避免签名 URL 变化触发图片重复加载。
      // 仅当旧 URL 快过期时（60s 内）才允许用新 URL 刷新。
      gen.input_assets = mergeAssetURLs(existing.input_assets, gen.input_assets)
      gen.output_assets = mergeAssetURLs(existing.output_assets, gen.output_assets)
      generations.value[idx] = gen
    } else {
      generations.value.unshift(gen)
    }
  }

  /**
   * 将指定 generationId 在前端标记为 'timeout' 虚拟状态。
   *
   * 触发场景：
   *   - 轮询超过 POLL_MAX_DURATION_MS 仍未到达终态
   *   - 连续失败次数达到 POLL_MAX_FAILURES
   *
   * 该状态不会同步到后端，仅用于停止轮询并提示用户重试。
   */
  function markGenerationTimeout(id: number) {
    const idx = generations.value.findIndex((g) => g.id === id)
    if (idx >= 0) {
      generations.value[idx] = {
        ...generations.value[idx],
        status: 'timeout' as ImageGenerationStatus,
      }
    }
  }

  // ==================== Conversations ====================

  async function fetchConversations(
    params: ImageConversationListParams = {},
    options?: { signal?: AbortSignal }
  ): Promise<PaginatedResponse<ImageConversation>> {
    conversationsLoading.value = true
    try {
      const page = params.page ?? conversationsPage.value
      const pageSize = params.page_size ?? conversationsPageSize.value
      const resp = await imageGenerationAPI.listConversations(
        { ...params, page, page_size: pageSize },
        options
      )
      conversations.value = resp.items
      conversationsTotal.value = resp.total
      conversationsPage.value = resp.page
      conversationsPageSize.value = resp.page_size
      return resp
    } catch (err) {
      setLastError(err)
      throw err
    } finally {
      conversationsLoading.value = false
    }
  }

  async function selectConversation(id: number | null): Promise<void> {
    // 切换会话前停止所有轮询并清空输入资产
    stopAllPolling()
    pendingInputAssets.value = []
    isDraftConversation.value = false

    if (id == null) {
      currentConversation.value = null
      generations.value = []
      return
    }

    // 加载会话详情和生成列表
    generationsLoading.value = true
    try {
      // 并行加载会话详情和生成列表
      const [conv, gens] = await Promise.all([
        imageGenerationAPI.getConversation(id),
        imageGenerationAPI.listGenerationsByConversation(id).catch((err) => {
          // 404 表示新会话尚无历史记录，当作空数组处理；其他错误向上抛出
          if (err?.response?.status === 404) return [] as ImageGeneration[]
          throw err
        }),
      ])
      currentConversation.value = conv
      generations.value = gens
      // 自动恢复进行中任务的轮询（仅对运行态任务；timeout 等本地虚拟终态不重启）
      for (const gen of gens) {
        if (isRunningStatus(gen.status)) {
          schedulePoll(gen.id)
        }
      }
    } catch (err) {
      setLastError(err)
      throw err
    } finally {
      generationsLoading.value = false
    }
  }

  async function fetchGenerationsByConversation(
    conversationId: number,
    params?: { page?: number; page_size?: number }
  ): Promise<ImageGeneration[]> {
    generationsLoading.value = true
    try {
      const items = await imageGenerationAPI.listGenerationsByConversation(
        conversationId,
        params
      )
      generations.value = items
      // 自动恢复进行中任务的轮询（仅对运行态任务）
      for (const gen of items) {
        if (isRunningStatus(gen.status)) {
          schedulePoll(gen.id)
        }
      }
      return items
    } catch (err) {
      setLastError(err)
      throw err
    } finally {
      generationsLoading.value = false
    }
  }

  async function createConversation(title?: string): Promise<ImageConversation> {
    const conv = await imageGenerationAPI.createConversation(title ? { title } : {})
    conversations.value = [conv, ...conversations.value]
    conversationsTotal.value += 1
    return conv
  }

  /**
   * 开始一个"新会话"：仅在前端清空状态，不调 API 持久化。
   *
   * 设计原因：用户点击"新建会话"后如果直接退出或新建下一个，
   * 不应该留下空会话记录。真正的会话在首次提交生成任务时由
   * 后端 CreateGeneration 自动创建（标题取 prompt 前 30 字）。
   */
  function startNewConversation(): void {
    stopAllPolling()
    currentConversation.value = null
    generations.value = []
    pendingInputAssets.value = []
    isDraftConversation.value = true
  }

  async function updateConversation(id: number, title: string): Promise<ImageConversation> {
    const updated = await imageGenerationAPI.updateConversation(id, { title })
    if (currentConversation.value?.id === id) {
      currentConversation.value = updated
    }
    const idx = conversations.value.findIndex((c) => c.id === id)
    if (idx >= 0) {
      conversations.value[idx] = updated
    }
    return updated
  }

  async function deleteConversation(id: number): Promise<void> {
    await imageGenerationAPI.deleteConversation(id)
    // 从列表中移除（响应式更新左侧侧边栏）
    conversations.value = conversations.value.filter((c) => c.id !== id)
    if (conversationsTotal.value > 0) {
      conversationsTotal.value -= 1
    }
    // 若删除的是当前会话，清空相关状态并进入 draft 草稿态
    if (currentConversation.value?.id === id) {
      stopAllPolling()
      currentConversation.value = null
      isDraftConversation.value = true
      generations.value = []
      pendingInputAssets.value = []
    }
  }

  // ==================== Generations ====================

  /**
   * 创建生成任务。自动生成 idempotency key 并启动状态轮询。
   * 服务端立即返回 pending 状态的 generation。
   *
   * 若当前无选中会话（startNewConversation 后的 draft 状态），
   * 不传 conversation_id，由后端自动创建会话（标题取 prompt 前 30 字）。
   * 拿到 generation 后用 gen.conversation_id 拉取真实会话详情并加入列表。
   *
   * 单轮约束：若当前会话已存在 pending/queued/processing/succeeded 状态的任务，
   * 不发起请求，直接抛出携带 reason=IMAGE_TASK_ALREADY_RUNNING 的错误，
   * 让调用方映射到友好提示。failed/canceled 视为终态失败，允许重试。
   *
   * 双击/并发保护：creatingGeneration 标志在请求期间置位，重复调用直接抛错，
   * 防止 Enter 键连按或按钮快速双击创建重复任务。后端同时强校验，
   * 即使绕过前端（直接调接口/页面刷新）也会返回 409。
   */
  async function createGeneration(
    payload: CreateImageGenerationRequest
  ): Promise<ImageGeneration> {
    // 双击/并发保护：同一时刻只允许一个 createGeneration 进行中
    if (creatingGeneration.value) {
      throw {
        status: 409,
        code: 'IMAGE_TASK_ALREADY_RUNNING',
        reason: 'IMAGE_TASK_ALREADY_RUNNING',
        message: 'a generation task already exists in this conversation',
      }
    }

    // 前端预校验：当前会话已存在有效任务（非 failed/canceled）时直接拒绝
    if (currentConversation.value !== null && hasActiveOrSucceededGeneration.value) {
      throw {
        status: 409,
        code: 'IMAGE_TASK_ALREADY_RUNNING',
        reason: 'IMAGE_TASK_ALREADY_RUNNING',
        message: 'a generation task already exists in this conversation',
      }
    }

    creatingGeneration.value = true
    try {
      const idempotencyKey = makeIdempotencyKey()
      // draft 状态：不传 conversation_id，让后端自动创建会话
      const isDraft = currentConversation.value === null
      const reqPayload: CreateImageGenerationRequest = {
        ...payload,
        conversation_id: isDraft ? null : (payload.conversation_id ?? null),
      }
      const gen = await imageGenerationAPI.createGeneration(reqPayload, idempotencyKey)
      upsertGeneration(gen)
      if (isRunningStatus(gen.status)) {
        schedulePoll(gen.id)
      }
      // draft → 真实会话：拉取会话详情并加入列表头部
      // 已存在会话：后端可能更新了标题（默认"新会话"→ prompt 前缀），需刷新
      if (gen.conversation_id) {
        try {
          const conv = await imageGenerationAPI.getConversation(gen.conversation_id)
          currentConversation.value = conv
          isDraftConversation.value = false
          const idx = conversations.value.findIndex((c) => c.id === conv.id)
          if (idx >= 0) {
            conversations.value[idx] = conv
          } else {
            conversations.value = [conv, ...conversations.value]
            conversationsTotal.value += 1
          }
        } catch (err) {
          // 会话详情拉取失败不影响 generation 流程，记录错误即可
          setLastError(err)
        }
      }
      // 提交后清空待用输入资产
      pendingInputAssets.value = []
      return gen
    } catch (err) {
      setLastError(err)
      throw err
    } finally {
      creatingGeneration.value = false
    }
  }

  async function fetchGeneration(id: number): Promise<ImageGeneration> {
    const gen = await imageGenerationAPI.getGeneration(id)
    upsertGeneration(gen)
    return gen
  }

  /**
   * 列出当前用户的全部生成任务（跨会话视图，带分页/筛选）。
   * 注意：此方法不会覆盖 currentConversation 下的 generations。
   */
  async function listGenerations(
    params: ImageGenerationListParams = {}
  ): Promise<PaginatedResponse<ImageGeneration>> {
    return imageGenerationAPI.listGenerations(params)
  }

  async function deleteGeneration(id: number): Promise<void> {
    await imageGenerationAPI.deleteGeneration(id)
    clearPollTimer(id)
    setActive(id, false)
    generations.value = generations.value.filter((g) => g.id !== id)
  }

  async function refreshGenerationAssets(id: number): Promise<ImageAsset[]> {
    const assets = await imageGenerationAPI.getGenerationAssets(id)
    const idx = generations.value.findIndex((g) => g.id === id)
    if (idx >= 0) {
      generations.value[idx] = {
        ...generations.value[idx],
        output_assets: assets,
      }
    }
    return assets
  }

  // ==================== Polling ====================
  //
  // 轮询控制器设计：
  //   - 每个 generationId 最多一个 PollContext（timer + abortController + 计数器）
  //   - schedulePoll 幂等：检测到活跃 context（timer 或 inFlight）直接返回，避免重复注册
  //   - 单次请求超时 15s（POLL_REQUEST_TIMEOUT_MS）+ abort signal，超时进入失败退避
  //   - 整体超时 5 分钟（POLL_MAX_DURATION_MS），超过后将任务标记为 'timeout' 虚拟状态
  //   - 失败退避：1s→2s→8s，连续失败 POLL_MAX_FAILURES 次后停止并标记 'timeout'
  //   - clearPollTimer 同步 abort 在飞请求，避免切换会话后旧请求污染 store

  /**
   * 启动一个 generation 的状态轮询。
   *
   * 幂等：若该 generationId 已存在活跃轮询器（timer 已调度或请求在飞），直接返回。
   * 调用方：createGeneration 创建后 / selectConversation 加载历史任务时 / fetchGenerationsByConversation。
   */
  function schedulePoll(generationId: number) {
    const existing = pollContexts.get(generationId)
    if (existing && (existing.timer !== null || existing.inFlight) && !existing.stopped) {
      // 已有活跃轮询器，避免重复注册
      return
    }
    const ctx: PollContext = {
      timer: null,
      abortController: null,
      startedAt: Date.now(),
      failureCount: 0,
      lastDelay: 0,
      inFlight: false,
      stopped: false,
    }
    pollContexts.set(generationId, ctx)
    setActive(generationId, true)
    scheduleNextPoll(generationId, POLL_INTERVAL_MIN_MS)
  }

  /**
   * 调度下一次轮询。在调度前会检查整体超时；超时则停止并标记 'timeout'。
   */
  function scheduleNextPoll(generationId: number, delayMs: number) {
    const ctx = pollContexts.get(generationId)
    if (!ctx || ctx.stopped) return

    // 整体超时检查：超过 POLL_MAX_DURATION_MS 后停止轮询并标记 timeout
    if (isPollDurationExceeded(ctx.startedAt, Date.now())) {
      markGenerationTimeout(generationId)
      clearPollTimer(generationId)
      setActive(generationId, false)
      return
    }

    ctx.lastDelay = delayMs
    ctx.timer = setTimeout(() => {
      const c = pollContexts.get(generationId)
      if (!c || c.stopped) return
      c.timer = null
      // 防御性：上次请求未结束（理论不应发生，因为 inFlight 时不会再调度）
      if (c.inFlight) return
      void pollOnce(generationId)
    }, delayMs)
  }

  /**
   * 执行一次轮询请求。
   *
   * - 使用独立 AbortController，clearPollTimer 时可中断
   * - 单次请求 15s 超时
   * - 成功：重置 failureCount，upsertGeneration，若到终态则停止；否则按指数退避调度下次
   * - 失败：failureCount++，达到 POLL_MAX_FAILURES 后停止并标记 'timeout'；否则退避重试
   * - abort（用户切换会话/卸载）：直接退出，不调度下次
   */
  async function pollOnce(generationId: number) {
    const ctx = pollContexts.get(generationId)
    if (!ctx || ctx.stopped) return

    const abortController = new AbortController()
    ctx.abortController = abortController
    ctx.inFlight = true

    try {
      const gen = await imageGenerationAPI.getGeneration(generationId, {
        signal: abortController.signal,
        timeout: POLL_REQUEST_TIMEOUT_MS,
      })
      // 请求期间可能已被停止
      const currentCtx = pollContexts.get(generationId)
      if (!currentCtx || currentCtx.stopped) return

      upsertGeneration(gen)
      currentCtx.failureCount = 0 // 成功后重置失败计数

      if (isTerminalStatus(gen.status)) {
        clearPollTimer(generationId)
        setActive(generationId, false)
        return
      }

      // 仍在运行态：按成功退避（1s→2s→5s 封顶）调度下次
      const nextDelay = nextPollDelay(currentCtx.lastDelay, true)
      scheduleNextPoll(generationId, nextDelay)
    } catch (err: any) {
      const currentCtx = pollContexts.get(generationId)
      if (!currentCtx || currentCtx.stopped) return

      // abort（用户主动停止）：不视为失败，直接退出
      if (err?.code === 'ERR_CANCELED' || abortController.signal.aborted) {
        return
      }

      currentCtx.failureCount += 1
      setLastError(err)

      // 连续失败达到阈值：停止轮询并标记 timeout
      if (currentCtx.failureCount >= POLL_MAX_FAILURES) {
        markGenerationTimeout(generationId)
        clearPollTimer(generationId)
        setActive(generationId, false)
        return
      }

      // 失败退避（2s→4s→8s 封顶）
      const nextDelay = nextPollDelay(currentCtx.lastDelay, false)
      scheduleNextPoll(generationId, nextDelay)
    } finally {
      const currentCtx = pollContexts.get(generationId)
      if (currentCtx) {
        currentCtx.inFlight = false
        currentCtx.abortController = null
      }
    }
  }

  /** 停止所有进行中的轮询（用于切换会话/登出/卸载） */
  function stopAllPolling() {
    for (const id of Array.from(pollContexts.keys())) {
      clearPollTimer(id)
    }
    activeGenerationIds.value = new Set()
  }

  /**
   * 停止指定 generationId 的轮询（公开 API，便于组件精确停止）。
   */
  function stopPoll(generationId: number) {
    clearPollTimer(generationId)
    setActive(generationId, false)
  }

  // ==================== Asset Upload ====================

  /**
   * 完整资产上传 pipeline：presign → 直传 S3 → confirm。
   * 成功后资产会加入 pendingInputAssets，供下一次 createGeneration 使用。
   *
   * @param file - 用户选择的图片文件
   * @returns 创建后的 ImageAsset
   */
  async function uploadInputAsset(file: File): Promise<ImageAsset> {
    uploadingAsset.value = true
    try {
      const presign = await imageGenerationAPI.presignUpload(file.type)
      await imageGenerationAPI.uploadToPresignedUrl(presign.upload_url, file, file.type)
      const asset = await imageGenerationAPI.confirmUpload({
        s3_key: presign.s3_key,
        mime_type: file.type,
        original_filename: file.name || null,
      })
      pendingInputAssets.value = [...pendingInputAssets.value, asset]
      return asset
    } catch (err) {
      setLastError(err)
      throw err
    } finally {
      uploadingAsset.value = false
    }
  }

  function removePendingInputAsset(assetId: number) {
    pendingInputAssets.value = pendingInputAssets.value.filter((a) => a.id !== assetId)
  }

  function clearPendingInputAssets() {
    pendingInputAssets.value = []
  }

  async function refreshAssetURL(assetId: number): Promise<string> {
    const resp = await imageGenerationAPI.refreshAssetURL(assetId)
    // 同步更新输出资产 URL
    for (const gen of generations.value) {
      if (!gen.output_assets) continue
      const idx = gen.output_assets.findIndex((a) => a.id === assetId)
      if (idx >= 0) {
        gen.output_assets[idx] = { ...gen.output_assets[idx], url: resp.url }
      }
    }
    // 同步 pendingInputAssets
    const pIdx = pendingInputAssets.value.findIndex((a) => a.id === assetId)
    if (pIdx >= 0) {
      pendingInputAssets.value[pIdx] = { ...pendingInputAssets.value[pIdx], url: resp.url }
    }
    return resp.url
  }

  // ==================== Cleanup ====================

  function reset() {
    stopAllPolling()
    conversations.value = []
    conversationsTotal.value = 0
    conversationsPage.value = 1
    currentConversation.value = null
    isDraftConversation.value = false
    generations.value = []
    pendingInputAssets.value = []
    lastError.value = null
  }

  return {
    // State
    conversations,
    conversationsTotal,
    conversationsPage,
    conversationsPageSize,
    conversationsLoading,
    currentConversation,
    generations,
    generationsLoading,
    isDraftConversation,
    activeGenerationIds,
    pendingInputAssets,
    creatingGeneration,
    uploadingAsset,
    lastError,
    // Getters
    hasConversations,
    hasActiveGeneration,
    hasActiveOrSucceededGeneration,
    totalConversationsPages,
    // Conversation actions
    fetchConversations,
    selectConversation,
    fetchGenerationsByConversation,
    createConversation,
    startNewConversation,
    updateConversation,
    deleteConversation,
    // Generation actions
    createGeneration,
    fetchGeneration,
    listGenerations,
    deleteGeneration,
    refreshGenerationAssets,
    // Polling
    schedulePoll,
    stopPoll,
    stopAllPolling,
    // Asset upload
    uploadInputAsset,
    removePendingInputAsset,
    clearPendingInputAssets,
    refreshAssetURL,
    // Misc
    reset,
  }
})
