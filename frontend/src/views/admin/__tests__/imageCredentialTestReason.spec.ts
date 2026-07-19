import { describe, it, expect } from 'vitest'

import zhImageCredentials from '@/i18n/locales/zh/admin/imageCredentials'
import enImageCredentials from '@/i18n/locales/en/admin/imageCredentials'
import {
  IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY,
  IMAGE_CREDENTIAL_TEST_REASON_NAMESPACE,
  getImageCredentialTestReasonI18nKey,
} from '../imageCredentialTestReason'

// ============================================================================
// 测试目标：
//   1. 7 种 reason 取值都有对应的 i18n key 映射
//   2. 解密失败 decrypt_failed 映射正确
//   3. 真正的上游认证失败 auth_failed 映射正确
//   4. 未知 reason 返回 null（前端回退到占位符 '—'）
//   5. zh/en locale 中所有 reason 对应文案都存在且非空
//   6. 命名空间前缀为 admin.imageCredentials.test
// ============================================================================

// 从 imageCredentials locale 中提取 test 子表
// locale 文件结构为：export default { imageCredentials: { ..., test: {...} } }
function getTestTable(locale: any): Record<string, string> {
  const root = locale.default ?? locale
  return root?.imageCredentials?.test ?? root?.test ?? {}
}

describe('IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY — 7 种 reason 映射', () => {
  it('包含 success 映射', () => {
    expect(IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY.success).toBe('reasonSuccess')
  })

  it('包含 decrypt_failed 映射（本次修复新增）', () => {
    expect(IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY.decrypt_failed).toBe('reasonDecryptFailed')
  })

  it('包含 auth_failed 映射（上游 401）', () => {
    expect(IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY.auth_failed).toBe('reasonAuthFailed')
  })

  it('包含 forbidden 映射（上游 403）', () => {
    expect(IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY.forbidden).toBe('reasonForbidden')
  })

  it('包含 rate_limited 映射（上游 429）', () => {
    expect(IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY.rate_limited).toBe('reasonRateLimited')
  })

  it('包含 timeout 映射', () => {
    expect(IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY.timeout).toBe('reasonTimeout')
  })

  it('包含 upstream_error 映射', () => {
    expect(IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY.upstream_error).toBe('reasonUpstreamError')
  })

  it('命名空间前缀为 admin.imageCredentials.test', () => {
    expect(IMAGE_CREDENTIAL_TEST_REASON_NAMESPACE).toBe('admin.imageCredentials.test')
  })
})

describe('getImageCredentialTestReasonI18nKey', () => {
  it('success → reasonSuccess', () => {
    expect(getImageCredentialTestReasonI18nKey('success')).toBe('reasonSuccess')
  })

  it('decrypt_failed → reasonDecryptFailed', () => {
    expect(getImageCredentialTestReasonI18nKey('decrypt_failed')).toBe('reasonDecryptFailed')
  })

  it('auth_failed → reasonAuthFailed', () => {
    expect(getImageCredentialTestReasonI18nKey('auth_failed')).toBe('reasonAuthFailed')
  })

  it('forbidden → reasonForbidden', () => {
    expect(getImageCredentialTestReasonI18nKey('forbidden')).toBe('reasonForbidden')
  })

  it('rate_limited → reasonRateLimited', () => {
    expect(getImageCredentialTestReasonI18nKey('rate_limited')).toBe('reasonRateLimited')
  })

  it('timeout → reasonTimeout', () => {
    expect(getImageCredentialTestReasonI18nKey('timeout')).toBe('reasonTimeout')
  })

  it('upstream_error → reasonUpstreamError', () => {
    expect(getImageCredentialTestReasonI18nKey('upstream_error')).toBe('reasonUpstreamError')
  })

  it('未知 reason 返回 null', () => {
    expect(getImageCredentialTestReasonI18nKey('unknown_reason')).toBeNull()
  })

  it('空字符串返回 null', () => {
    expect(getImageCredentialTestReasonI18nKey('')).toBeNull()
  })

  it('undefined 返回 null', () => {
    expect(getImageCredentialTestReasonI18nKey(undefined)).toBeNull()
  })

  it('null 返回 null', () => {
    expect(getImageCredentialTestReasonI18nKey(null)).toBeNull()
  })
})

describe('i18n 文案 — 管理后台凭据测试 reason', () => {
  it('中文 locale 包含 reason 字段标签', () => {
    const test = getTestTable(zhImageCredentials)
    expect(test.reason).toBe('失败原因')
  })

  it('英文 locale 包含 reason 字段标签', () => {
    const test = getTestTable(enImageCredentials)
    expect(test.reason).toBe('Failure Reason')
  })

  it('中文 locale - decrypt_failed 文案明确说明未调用上游', () => {
    const test = getTestTable(zhImageCredentials)
    expect(test.reasonDecryptFailed).toBeDefined()
    expect(test.reasonDecryptFailed).toContain('本地解密失败')
    expect(test.reasonDecryptFailed).toContain('未调用上游')
  })

  it('英文 locale - decrypt_failed 文案明确说明未调用上游', () => {
    const test = getTestTable(enImageCredentials)
    expect(test.reasonDecryptFailed).toBeDefined()
    expect(test.reasonDecryptFailed.toLowerCase()).toContain('local decryption failed')
    expect(test.reasonDecryptFailed.toLowerCase()).toContain('upstream not called')
  })

  it('中文 locale - auth_failed 文案明确说明 401', () => {
    const test = getTestTable(zhImageCredentials)
    expect(test.reasonAuthFailed).toBeDefined()
    expect(test.reasonAuthFailed).toContain('401')
  })

  it('中文 locale - forbidden 文案明确说明 403', () => {
    const test = getTestTable(zhImageCredentials)
    expect(test.reasonForbidden).toBeDefined()
    expect(test.reasonForbidden).toContain('403')
  })

  it('中文 locale - rate_limited 文案明确说明 429', () => {
    const test = getTestTable(zhImageCredentials)
    expect(test.reasonRateLimited).toBeDefined()
    expect(test.reasonRateLimited).toContain('429')
  })
})

describe('所有 reason 的 i18n key 在 zh/en locale 中均存在', () => {
  const reasonKeys = Object.values(IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY).concat(['reason'])

  for (const key of reasonKeys) {
    it(`zh locale 包含 admin.imageCredentials.test.${key}`, () => {
      const test = getTestTable(zhImageCredentials)
      expect(test).toHaveProperty(key)
      expect(typeof test[key]).toBe('string')
      expect((test[key] as string).length).toBeGreaterThan(0)
    })

    it(`en locale 包含 admin.imageCredentials.test.${key}`, () => {
      const test = getTestTable(enImageCredentials)
      expect(test).toHaveProperty(key)
      expect(typeof test[key]).toBe('string')
      expect((test[key] as string).length).toBeGreaterThan(0)
    })
  }
})
