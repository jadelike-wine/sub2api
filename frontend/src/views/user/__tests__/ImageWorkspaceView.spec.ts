import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

import zhDashboard from '@/i18n/locales/zh/dashboard'
import enDashboard from '@/i18n/locales/en/dashboard'
import type { ImageGeneration } from '@/types'

// ============================================================================
// Mock useImageGenerationStore 与 useI18n，避免依赖真实 store / i18n 实例。
// 仅验证 ImageWorkspaceView 在不同 generation.status 下的 UI 渲染。
// ============================================================================

// dashboard.ts 使用 `export default {...}`，import X from '...' 拿到的就是对象本身，
// 不应再访问 .default（否则为 undefined → 退化为空对象 → t() 全部返回 key 原文）
const statusTableZh = zhDashboard?.aiImage?.workspace?.status ?? {}
const statusTableEn = enDashboard?.aiImage?.workspace?.status ?? {}
const generationsZh = zhDashboard?.aiImage?.workspace?.generations ?? {}

const mockStoreState = vi.hoisted(() => ({
  generations: [] as ImageGeneration[],
  hasActiveOrSucceeded: false,
  currentConversation: null as { id: number; title: string } | null,
  isDraft: false,
  creating: false,
  uploading: false,
  pendingInputAssets: [] as any[],
  conversationsLoading: false,
  conversations: [] as any[],
  conversationsTotal: 0,
  conversationsPage: 1,
  conversationsPageSize: 20,
  generationsLoading: false,
  activeGenerationIds: new Set<number>(),
  lastError: null as string | null,
}))

vi.mock('@/stores/imageGeneration', () => ({
  useImageGenerationStore: () => ({
    // State
    conversations: mockStoreState.conversations,
    conversationsTotal: mockStoreState.conversationsTotal,
    conversationsPage: mockStoreState.conversationsPage,
    conversationsPageSize: mockStoreState.conversationsPageSize,
    conversationsLoading: mockStoreState.conversationsLoading,
    currentConversation: mockStoreState.currentConversation,
    generations: mockStoreState.generations,
    generationsLoading: mockStoreState.generationsLoading,
    isDraftConversation: mockStoreState.isDraft,
    activeGenerationIds: mockStoreState.activeGenerationIds,
    pendingInputAssets: mockStoreState.pendingInputAssets,
    creatingGeneration: mockStoreState.creating,
    uploadingAsset: mockStoreState.uploading,
    lastError: mockStoreState.lastError,
    // Getters
    hasConversations: mockStoreState.conversations.length > 0,
    hasActiveGeneration: mockStoreState.activeGenerationIds.size > 0,
    hasActiveOrSucceededGeneration: mockStoreState.hasActiveOrSucceeded,
    totalConversationsPages: 1,
    // Actions（这些测试不会真正调用，给出空实现即可）
    fetchConversations: vi.fn().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 }),
    selectConversation: vi.fn(),
    fetchGenerationsByConversation: vi.fn(),
    createConversation: vi.fn(),
    startNewConversation: vi.fn(),
    updateConversation: vi.fn(),
    deleteConversation: vi.fn(),
    createGeneration: vi.fn(),
    fetchGeneration: vi.fn(),
    listGenerations: vi.fn(),
    deleteGeneration: vi.fn(),
    refreshGenerationAssets: vi.fn(),
    schedulePoll: vi.fn(),
    stopPoll: vi.fn(),
    stopAllPolling: vi.fn(),
    uploadInputAsset: vi.fn(),
    removePendingInputAsset: vi.fn(),
    clearPendingInputAssets: vi.fn(),
    refreshAssetURL: vi.fn(),
    reset: vi.fn(),
  }),
}))

// Mock AppLayout 简化为 slot 渲染，避免依赖路由/store
vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: {
    name: 'AppLayout',
    template: '<div class="app-layout-mock"><slot /></div>',
  },
}))

// Mock StatusBadge：渲染原始 status 文本，便于断言组件被使用
vi.mock('@/components/image/StatusBadge.vue', () => ({
  default: {
    name: 'StatusBadge',
    props: ['status'],
    template: '<span class="status-badge-mock" :data-status="status">{{ status }}</span>',
  },
}))

// Mock el-image（Element Plus 组件，jsdom 下不需要真实渲染）
vi.mock('element-plus', () => ({
  ElImage: {
    name: 'ElImage',
    template: '<div class="el-image-mock"><slot /></div>',
  },
}))

function buildFakeT(locale: 'zh' | 'en') {
  const status = locale === 'zh' ? statusTableZh : statusTableEn
  const gens = generationsZh
  // 运行态统一文案：queued/pending/processing 在主视觉区都显示 processingHint
  const processingHintZh = '排队生成中'
  const processingHintEn = 'Generating, please wait...'
  return (key: string) => {
    if (key.startsWith('aiImage.workspace.status.')) {
      return status[key.split('.').pop() as string] ?? key
    }
    if (key === 'aiImage.workspace.generations.timeoutHint') {
      return gens.timeoutHint ?? key
    }
    if (key === 'aiImage.workspace.generations.processingHint') {
      return locale === 'zh' ? processingHintZh : processingHintEn
    }
    if (key === 'aiImage.workspace.composer.typeTextToImage') return '文生图'
    if (key === 'aiImage.workspace.composer.generating') return '生成中'
    if (key === 'aiImage.workspace.generations.duration') return '耗时 {ms} ms'
    // 其他 key 返回 key 自身（不影响断言）
    return key
  }
}

// AppLayout stub 必须渲染 <slot /> 否则子内容不会出现在 wrapper.html() 中
const slotStub = { template: '<div class="layout-stub"><slot /></div>' }

function mountView(locale: 'zh' | 'en' = 'zh') {
  // 每次重新 mock 前先清掉模块缓存与之前的 doMock 注册，
  // 确保不同 locale 的 t() mock 不会相互污染。
  vi.resetModules()
  vi.doUnmock('vue-i18n')
  const tMock = vi.fn(buildFakeT(locale))
  vi.doMock('vue-i18n', () => ({
    useI18n: () => ({ t: tMock }),
  }))
  return import('../ImageWorkspaceView.vue').then((mod) => {
    const View = mod.default
    return mount(View, {
      global: {
        stubs: {
          // 必须使用带 slot 的 stub，否则内容被吞掉
          AppLayout: slotStub,
        },
      },
    })
  })
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
    status: 'queued',
    duration_ms: 0,
    created_at: '2026-07-18T00:00:00Z',
    updated_at: '2026-07-18T00:00:00Z',
    ...overrides,
  }
}

describe('ImageWorkspaceView — 运行态 UI 渲染', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.doUnmock('vue-i18n')
    // 重置 mockStoreState
    mockStoreState.generations = []
    mockStoreState.hasActiveOrSucceeded = false
    mockStoreState.currentConversation = { id: 1, title: 'test' }
    mockStoreState.isDraft = false
    mockStoreState.creating = false
    mockStoreState.uploading = false
    mockStoreState.pendingInputAssets = []
    mockStoreState.conversationsLoading = false
    mockStoreState.conversations = [{ id: 1, title: 'test' }]
    mockStoreState.generationsLoading = false
  })

  // --- 场景 1：queued 显示 loading 动画 + "排队中" ---

  it('queued 状态显示 spinner + 「排队生成中」+ 骨架屏', async () => {
    mockStoreState.generations = [makeGen({ id: 1, status: 'queued' })]
    mockStoreState.hasActiveOrSucceeded = true // queued 阻止再次提交，composer 隐藏

    const wrapper = await mountView('zh')
    await flushPromises()

    const html = wrapper.html()
    // 包含 animate-spin 的 svg
    expect(html).toContain('animate-spin')
    // 运行态统一文案
    expect(html).toContain('排队生成中')
    // 包含骨架屏占位（aspect-square）
    expect(html).toContain('aspect-square')
  })

  // --- 场景 2：processing 显示 loading + "排队生成中" ---

  it('processing 状态显示 spinner + 「排队生成中」', async () => {
    mockStoreState.generations = [makeGen({ id: 2, status: 'processing' })]
    mockStoreState.hasActiveOrSucceeded = true

    const wrapper = await mountView('zh')
    await flushPromises()

    const html = wrapper.html()
    expect(html).toContain('animate-spin')
    expect(html).toContain('排队生成中')
  })

  // --- 场景 3：pending 显示 loading + "排队生成中" ---

  it('pending 状态显示 spinner + 「排队生成中」', async () => {
    mockStoreState.generations = [makeGen({ id: 3, status: 'pending' })]
    mockStoreState.hasActiveOrSucceeded = true

    const wrapper = await mountView('zh')
    await flushPromises()

    const html = wrapper.html()
    expect(html).toContain('animate-spin')
    expect(html).toContain('排队生成中')
  })

  // --- 场景 4：timeout 显示"生成超时，请重试"与提示 ---

  it('timeout 状态显示「生成超时，请重试」+ 提示文案', async () => {
    mockStoreState.generations = [makeGen({ id: 4, status: 'timeout' })]
    // timeout 不在 CONVERSATION_BLOCKING_STATUSES 中，composer 应重新显示
    mockStoreState.hasActiveOrSucceeded = false

    const wrapper = await mountView('zh')
    await flushPromises()

    const html = wrapper.html()
    expect(html).toContain('生成超时，请重试')
    // timeoutHint 提示文案
    expect(html).toContain('任务长时间未返回结果')
    // 不应显示 loading spinner（运行态动画）
    // timeout 卡片本身不带 animate-spin 类
  })

  // --- 场景 5：英文 locale 状态文案正确 ---

  it('英文 locale queued 显示 "Generating, please wait..."', async () => {
    mockStoreState.generations = [makeGen({ id: 5, status: 'queued' })]
    mockStoreState.hasActiveOrSucceeded = true

    const wrapper = await mountView('en')
    await flushPromises()

    const html = wrapper.html()
    expect(html).toContain('Generating, please wait...')
    // 不应显示中文
    expect(html).not.toContain('排队生成中')
  })

  it('英文 locale timeout 显示 "Generation timed out, please retry"', async () => {
    mockStoreState.generations = [makeGen({ id: 6, status: 'timeout' })]
    mockStoreState.hasActiveOrSucceeded = false

    const wrapper = await mountView('en')
    await flushPromises()

    const html = wrapper.html()
    expect(html).toContain('Generation timed out, please retry')
  })

  // --- 场景 6：queued -> processing -> succeeded 流程中 UI 文案切换 ---
  //
  // 通过修改 mockStoreState.generations 重新 mount 验证三种状态各自渲染正确。
  // 真实的状态流转由 store 轮询驱动，此处只验证 View 对不同 status 的响应。

  it('同一组件在不同 status 下渲染对应文案', async () => {
    // queued
    mockStoreState.generations = [makeGen({ id: 7, status: 'queued' })]
    mockStoreState.hasActiveOrSucceeded = true
    let wrapper = await mountView('zh')
    await flushPromises()
    expect(wrapper.html()).toContain('排队生成中')

    // 重新 mount 模拟 status 变为 processing
    vi.resetModules()
    mockStoreState.generations = [makeGen({ id: 7, status: 'processing' })]
    wrapper = await mountView('zh')
    await flushPromises()
    expect(wrapper.html()).toContain('排队生成中')

    // 重新 mount 模拟 status 变为 succeeded
    vi.resetModules()
    mockStoreState.generations = [
      makeGen({
        id: 7,
        status: 'succeeded',
        output_assets: [],
        duration_ms: 1000,
      }),
    ]
    mockStoreState.hasActiveOrSucceeded = true
    wrapper = await mountView('zh')
    await flushPromises()
    // succeeded 状态不显示 spinner（运行态动画）
    // 但卡片头部 StatusBadge 仍会渲染 succeeded
    expect(wrapper.html()).toContain('succeeded')
  })
})
