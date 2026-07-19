import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'

import zhDashboard from '@/i18n/locales/zh/dashboard'
import enDashboard from '@/i18n/locales/en/dashboard'

// ============================================================================
// 测试目标：
//   1. queued 显示「排队中」，不显示原始 key 字符串
//   2. processing 显示「生成中」
//   3. succeeded 和 completed 均显示「已完成」
//   4. 未知状态显示「未知状态」（不显示原始 key 或 status 字符串）
//   5. zh/en 切换后所有状态文案正确
//   6. queued 任务徽章样式正常渲染
//
// 说明：通过 mock useI18n 直接模拟 vue-i18n 的 t() 行为，
// 既验证 StatusBadge 调用了 getImageStatusI18nKey（不直接拼接 status），
// 又避免 vue-i18n runtime 在 jsdom 测试环境的 messages 注入差异。
// zh/en locale 文案本身由 imageStatusMessages.spec.ts 直接校验。
// ============================================================================

// 从 dashboard locale 中提取 aiImage.workspace.status 子表
function getStatusTable(locale: any): Record<string, string> {
  return locale.default?.aiImage?.workspace?.status
    ?? locale.aiImage?.workspace?.status
    ?? {}
}

const zhStatus = getStatusTable(zhDashboard)
const enStatus = getStatusTable(enDashboard)

// 模拟 vue-i18n 的 t()：key 存在时返回文案，不存在时返回 key（与 vue-i18n 行为一致）
function buildFakeT(statusTable: Record<string, string>) {
  return (key: string) => statusTable[key.split('.').pop() as string] ?? key
}

function mountBadge(status: string | undefined, locale: 'zh' | 'en' = 'zh') {
  // 动态导入前先 mock，避免 StatusBadge 内部 useI18n 在 mount 时拿到真实实例
  const statusTable = locale === 'zh' ? zhStatus : enStatus
  const tMock = vi.fn(buildFakeT(statusTable))

  vi.doMock('vue-i18n', () => ({
    useI18n: () => ({ t: tMock }),
  }))

  // 动态导入确保 mock 生效
  return import('@/components/image/StatusBadge.vue').then((mod) => {
    const StatusBadge = mod.default
    return mount(StatusBadge, {
      props: { status: (status ?? '') as any },
    })
  })
}

describe('StatusBadge — 中文文案渲染', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.doUnmock('vue-i18n')
  })

  it('queued 显示「排队中」，不显示原始 key', async () => {
    const wrapper = await mountBadge('queued', 'zh')
    const text = wrapper.text()
    expect(text).toContain('排队中')
    expect(text).not.toContain('aiImage.workspace.status.queued')
    expect(text).not.toMatch(/\bqueued\b/)
  })

  it('pending 显示「等待中」', async () => {
    const wrapper = await mountBadge('pending', 'zh')
    expect(wrapper.text()).toContain('等待中')
  })

  it('processing 显示「生成中」', async () => {
    const wrapper = await mountBadge('processing', 'zh')
    expect(wrapper.text()).toContain('生成中')
  })

  it('succeeded 显示「已完成」', async () => {
    const wrapper = await mountBadge('succeeded', 'zh')
    expect(wrapper.text()).toContain('已完成')
  })

  // 别名兼容：completed 与 succeeded 都显示「已完成」
  it('completed 显示「已完成」（succeeded 别名）', async () => {
    const wrapper = await mountBadge('completed', 'zh')
    expect(wrapper.text()).toContain('已完成')
  })

  it('failed 显示「生成失败」', async () => {
    const wrapper = await mountBadge('failed', 'zh')
    expect(wrapper.text()).toContain('生成失败')
  })

  it('canceled 显示「已取消」', async () => {
    const wrapper = await mountBadge('canceled', 'zh')
    expect(wrapper.text()).toContain('已取消')
  })

  // 关键：未知状态必须显示兜底文案，绝不能显示原始 status 字符串或 key
  it('未知状态显示「未知状态」，不显示原始 key 或 status', async () => {
    const wrapper = await mountBadge('totally_unknown_status', 'zh')
    const text = wrapper.text()
    expect(text).toContain('未知状态')
    expect(text).not.toContain('aiImage.workspace.status.unknown')
    expect(text).not.toContain('totally_unknown_status')
  })

  it('空字符串状态显示「未知状态」', async () => {
    const wrapper = await mountBadge('', 'zh')
    expect(wrapper.text()).toContain('未知状态')
  })
})

describe('StatusBadge — 英文文案渲染', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.doUnmock('vue-i18n')
  })

  it('queued 显示 "Queued"，不显示原始 key', async () => {
    const wrapper = await mountBadge('queued', 'en')
    const text = wrapper.text()
    expect(text).toContain('Queued')
    expect(text).not.toContain('aiImage.workspace.status.queued')
  })

  it('processing 显示 "Generating"', async () => {
    const wrapper = await mountBadge('processing', 'en')
    expect(wrapper.text()).toContain('Generating')
  })

  it('succeeded 显示 "Completed"', async () => {
    const wrapper = await mountBadge('succeeded', 'en')
    expect(wrapper.text()).toContain('Completed')
  })

  it('completed 显示 "Completed"（succeeded 别名）', async () => {
    const wrapper = await mountBadge('completed', 'en')
    expect(wrapper.text()).toContain('Completed')
  })

  it('failed 显示 "Failed"', async () => {
    const wrapper = await mountBadge('failed', 'en')
    expect(wrapper.text()).toContain('Failed')
  })

  it('canceled 显示 "Canceled"', async () => {
    const wrapper = await mountBadge('canceled', 'en')
    expect(wrapper.text()).toContain('Canceled')
  })

  it('未知状态显示 "Unknown"，不显示原始 key', async () => {
    const wrapper = await mountBadge('totally_unknown_status', 'en')
    const text = wrapper.text()
    expect(text).toContain('Unknown')
    expect(text).not.toContain('totally_unknown_status')
    expect(text).not.toContain('aiImage.workspace.status.unknown')
  })
})

describe('StatusBadge — 状态徽章样式与圆点颜色', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.doUnmock('vue-i18n')
  })

  // 验证 queued 任务不会因为没有样式分支而渲染异常
  it('queued 状态正常渲染，包含徽章容器和圆点', async () => {
    const wrapper = await mountBadge('queued', 'zh')
    const badge = wrapper.find('span.inline-flex')
    expect(badge.exists()).toBe(true)
    const dot = badge.find('span.h-1\\.5')
    expect(dot.exists()).toBe(true)
  })

  it('processing 圆点带 animate-pulse 类', async () => {
    const wrapper = await mountBadge('processing', 'zh')
    const dot = wrapper.find('span.h-1\\.5')
    expect(dot.classes()).toContain('animate-pulse')
  })

  it('succeeded 圆点为 bg-green-500', async () => {
    const wrapper = await mountBadge('succeeded', 'zh')
    const dot = wrapper.find('span.h-1\\.5')
    expect(dot.classes()).toContain('bg-green-500')
  })

  it('failed 圆点为 bg-red-500', async () => {
    const wrapper = await mountBadge('failed', 'zh')
    const dot = wrapper.find('span.h-1\\.5')
    expect(dot.classes()).toContain('bg-red-500')
  })

  it('completed 别名也使用绿色圆点', async () => {
    const wrapper = await mountBadge('completed', 'zh')
    const dot = wrapper.find('span.h-1\\.5')
    expect(dot.classes()).toContain('bg-green-500')
  })
})
