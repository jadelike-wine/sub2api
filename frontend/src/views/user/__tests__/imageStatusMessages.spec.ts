import { describe, it, expect } from 'vitest'

import zhDashboard from '@/i18n/locales/zh/dashboard'
import enDashboard from '@/i18n/locales/en/dashboard'
import {
  GENERATION_STATUS_TO_I18N_KEY,
  IMAGE_STATUS_I18N_NAMESPACE,
  IMAGE_STATUS_UNKNOWN_KEY,
  getImageStatusI18nKey,
} from '../imageStatusMessages'

// ============================================================================
// 测试目标：
//   1. 后端所有 status 都有 i18n key 映射
//   2. succeeded 和 completed 都映射到 'completed' key（别名兼容）
//   3. 未知状态返回 unknown key（不返回原始 status 拼接的 key）
//   4. zh/en locale 包含所有映射的 i18n key
//   5. zh 文案与用户期望一致（queued=排队中等）
//   6. en 文案与用户期望一致（queued=Queued 等）
//   7. 命名空间前缀正确
// ============================================================================

// 从 dashboard locale 中提取 aiImage.workspace.status 子表
function getStatusTable(locale: any): Record<string, string> {
  return locale.default?.aiImage?.workspace?.status
    ?? locale.aiImage?.workspace?.status
    ?? {}
}

describe('GENERATION_STATUS_TO_I18N_KEY — 后端状态映射', () => {
  it('queued → queued', () => {
    expect(GENERATION_STATUS_TO_I18N_KEY.queued).toBe('queued')
  })

  it('pending → pending', () => {
    expect(GENERATION_STATUS_TO_I18N_KEY.pending).toBe('pending')
  })

  it('processing → processing', () => {
    expect(GENERATION_STATUS_TO_I18N_KEY.processing).toBe('processing')
  })

  it('succeeded → completed（别名映射）', () => {
    expect(GENERATION_STATUS_TO_I18N_KEY.succeeded).toBe('completed')
  })

  it('completed → completed（succeeded 的别名兼容）', () => {
    expect(GENERATION_STATUS_TO_I18N_KEY.completed).toBe('completed')
  })

  it('failed → failed', () => {
    expect(GENERATION_STATUS_TO_I18N_KEY.failed).toBe('failed')
  })

  it('canceled → canceled', () => {
    expect(GENERATION_STATUS_TO_I18N_KEY.canceled).toBe('canceled')
  })
})

describe('IMAGE_STATUS_I18N_NAMESPACE / IMAGE_STATUS_UNKNOWN_KEY', () => {
  it('命名空间前缀为 aiImage.workspace.status', () => {
    expect(IMAGE_STATUS_I18N_NAMESPACE).toBe('aiImage.workspace.status')
  })

  it('未知状态兜底 key 为 unknown', () => {
    expect(IMAGE_STATUS_UNKNOWN_KEY).toBe('unknown')
  })
})

describe('getImageStatusI18nKey — 完整 i18n key 返回', () => {
  it('queued → aiImage.workspace.status.queued', () => {
    expect(getImageStatusI18nKey('queued')).toBe('aiImage.workspace.status.queued')
  })

  it('pending → aiImage.workspace.status.pending', () => {
    expect(getImageStatusI18nKey('pending')).toBe('aiImage.workspace.status.pending')
  })

  it('processing → aiImage.workspace.status.processing', () => {
    expect(getImageStatusI18nKey('processing')).toBe('aiImage.workspace.status.processing')
  })

  it('succeeded → aiImage.workspace.status.completed（别名）', () => {
    expect(getImageStatusI18nKey('succeeded')).toBe('aiImage.workspace.status.completed')
  })

  it('completed → aiImage.workspace.status.completed（别名）', () => {
    expect(getImageStatusI18nKey('completed')).toBe('aiImage.workspace.status.completed')
  })

  it('failed → aiImage.workspace.status.failed', () => {
    expect(getImageStatusI18nKey('failed')).toBe('aiImage.workspace.status.failed')
  })

  it('canceled → aiImage.workspace.status.canceled', () => {
    expect(getImageStatusI18nKey('canceled')).toBe('aiImage.workspace.status.canceled')
  })

  it('未知状态 → aiImage.workspace.status.unknown（不返回原始 status 拼接的 key）', () => {
    expect(getImageStatusI18nKey('some_unknown_status')).toBe('aiImage.workspace.status.unknown')
  })

  it('空字符串 → aiImage.workspace.status.unknown', () => {
    expect(getImageStatusI18nKey('')).toBe('aiImage.workspace.status.unknown')
  })

  it('undefined → aiImage.workspace.status.unknown', () => {
    expect(getImageStatusI18nKey(undefined)).toBe('aiImage.workspace.status.unknown')
  })

  it('null → aiImage.workspace.status.unknown', () => {
    expect(getImageStatusI18nKey(null)).toBe('aiImage.workspace.status.unknown')
  })
})

describe('i18n 文案 — zh 中文文案与用户期望一致', () => {
  const expected = {
    queued: '排队中',
    pending: '等待中',
    processing: '生成中',
    succeeded: '已完成',
    completed: '已完成',
    failed: '生成失败',
    canceled: '已取消',
    unknown: '未知状态',
  }

  for (const [key, value] of Object.entries(expected)) {
    it(`zh aiImage.workspace.status.${key} = "${value}"`, () => {
      const status = getStatusTable(zhDashboard)
      expect(status[key]).toBe(value)
    })
  }
})

describe('i18n 文案 — en 英文文案与用户期望一致', () => {
  const expected = {
    queued: 'Queued',
    pending: 'Pending',
    processing: 'Generating',
    succeeded: 'Completed',
    completed: 'Completed',
    failed: 'Failed',
    canceled: 'Canceled',
    unknown: 'Unknown',
  }

  for (const [key, value] of Object.entries(expected)) {
    it(`en aiImage.workspace.status.${key} = "${value}"`, () => {
      const status = getStatusTable(enDashboard)
      expect(status[key]).toBe(value)
    })
  }
})

describe('所有映射的 i18n key 在 zh/en locale 中均存在', () => {
  // 所有映射值 + unknown 兜底都必须在 locale 中存在
  const requiredKeys = Array.from(new Set(Object.values(GENERATION_STATUS_TO_I18N_KEY)))
    .concat([IMAGE_STATUS_UNKNOWN_KEY])

  for (const key of requiredKeys) {
    it(`zh locale 包含 aiImage.workspace.status.${key}`, () => {
      const status = getStatusTable(zhDashboard)
      expect(status).toHaveProperty(key)
      expect(typeof status[key]).toBe('string')
      expect((status[key] as string).length).toBeGreaterThan(0)
    })

    it(`en locale 包含 aiImage.workspace.status.${key}`, () => {
      const status = getStatusTable(enDashboard)
      expect(status).toHaveProperty(key)
      expect(typeof status[key]).toBe('string')
      expect((status[key] as string).length).toBeGreaterThan(0)
    })
  }
})
