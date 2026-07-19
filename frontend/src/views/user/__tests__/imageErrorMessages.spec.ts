import { describe, it, expect } from 'vitest'

import zhDashboard from '@/i18n/locales/zh/dashboard'
import enDashboard from '@/i18n/locales/en/dashboard'
import {
  IMAGE_ERROR_CODE_TO_I18N_KEY,
  IMAGE_ERROR_I18N_NAMESPACE,
  getImageErrorI18nKey,
} from '../imageErrorMessages'

// ============================================================================
// 测试目标：
//   1. IMAGE_ERROR_CODE_TO_I18N_KEY 包含 IMAGE_CREDENTIAL_DECRYPT_FAILED 映射
//   2. getImageErrorI18nKey 对 IMAGE_CREDENTIAL_DECRYPT_FAILED 返回 'credentialDecryptFailed'
//   3. getImageErrorI18nKey 对 IMAGE_PROVIDER_AUTH_FAILED 仍返回 'providerAuthFailed'（不影响现有映射）
//   4. 未知 error_code 返回 null
//   5. 空/undefined/null 返回 null
//   6. 中文 i18n 文案存在且与建议提示一致
//   7. 英文 i18n 文案存在且与建议提示一致
//   8. 所有映射的 i18n key 在 zh/en locale 中均存在（防止 key 缺失导致回退到 unknown）
// ============================================================================

// 从 dashboard locale 中提取 aiImage.workspace.errors 子表
function getErrorsTable(locale: any): Record<string, string> {
  // zhDashboard / enDashboard 默认导出形如 { dashboard: {...}, aiImage: {...}, ... }
  // aiImage.workspace.errors 即为目标错误文案表
  return locale.default?.aiImage?.workspace?.errors
    ?? locale.aiImage?.workspace?.errors
    ?? {}
}

describe('IMAGE_ERROR_CODE_TO_I18N_KEY — IMAGE_CREDENTIAL_DECRYPT_FAILED 映射', () => {
  it('包含 IMAGE_CREDENTIAL_DECRYPT_FAILED 条目', () => {
    expect(IMAGE_ERROR_CODE_TO_I18N_KEY).toHaveProperty('IMAGE_CREDENTIAL_DECRYPT_FAILED')
  })

  it('IMAGE_CREDENTIAL_DECRYPT_FAILED 映射到 credentialDecryptFailed i18n key', () => {
    expect(IMAGE_ERROR_CODE_TO_I18N_KEY.IMAGE_CREDENTIAL_DECRYPT_FAILED).toBe('credentialDecryptFailed')
  })

  it('IMAGE_PROVIDER_AUTH_FAILED 仍映射到 providerAuthFailed（不破坏现有映射）', () => {
    expect(IMAGE_ERROR_CODE_TO_I18N_KEY.IMAGE_PROVIDER_AUTH_FAILED).toBe('providerAuthFailed')
  })

  it('命名空间前缀为 aiImage.workspace.errors', () => {
    expect(IMAGE_ERROR_I18N_NAMESPACE).toBe('aiImage.workspace.errors')
  })
})

describe('getImageErrorI18nKey', () => {
  it('IMAGE_CREDENTIAL_DECRYPT_FAILED → credentialDecryptFailed', () => {
    expect(getImageErrorI18nKey('IMAGE_CREDENTIAL_DECRYPT_FAILED')).toBe('credentialDecryptFailed')
  })

  it('IMAGE_PROVIDER_AUTH_FAILED → providerAuthFailed', () => {
    expect(getImageErrorI18nKey('IMAGE_PROVIDER_AUTH_FAILED')).toBe('providerAuthFailed')
  })

  it('IMAGE_PROVIDER_RATE_LIMITED → providerRateLimited', () => {
    expect(getImageErrorI18nKey('IMAGE_PROVIDER_RATE_LIMITED')).toBe('providerRateLimited')
  })

  it('IMAGE_NO_AVAILABLE_CREDENTIAL → noCredential', () => {
    expect(getImageErrorI18nKey('IMAGE_NO_AVAILABLE_CREDENTIAL')).toBe('noCredential')
  })

  it('未知 error_code 返回 null', () => {
    expect(getImageErrorI18nKey('SOME_UNKNOWN_CODE')).toBeNull()
  })

  it('空字符串返回 null', () => {
    expect(getImageErrorI18nKey('')).toBeNull()
  })

  it('undefined 返回 null', () => {
    expect(getImageErrorI18nKey(undefined)).toBeNull()
  })

  it('null 返回 null', () => {
    expect(getImageErrorI18nKey(null)).toBeNull()
  })
})

describe('i18n 文案 — IMAGE_CREDENTIAL_DECRYPT_FAILED', () => {
  it('中文文案存在且与建议提示一致', () => {
    const errors = getErrorsTable(zhDashboard)
    expect(errors.credentialDecryptFailed).toBeDefined()
    expect(errors.credentialDecryptFailed).toBe('生图凭据无法解密，请联系管理员重新配置凭据')
  })

  it('英文文案存在且与建议提示一致', () => {
    const errors = getErrorsTable(enDashboard)
    expect(errors.credentialDecryptFailed).toBeDefined()
    expect(errors.credentialDecryptFailed).toBe('The image generation credential could not be decrypted. Please contact the administrator.')
  })

  it('IMAGE_PROVIDER_AUTH_FAILED 中文文案不受影响（仍为上游认证失败）', () => {
    const errors = getErrorsTable(zhDashboard)
    expect(errors.providerAuthFailed).toBe('上游凭据认证失败，请联系管理员')
  })

  it('IMAGE_PROVIDER_AUTH_FAILED 英文文案不受影响', () => {
    const errors = getErrorsTable(enDashboard)
    expect(errors.providerAuthFailed).toBe('Upstream authentication failed, please contact the admin')
  })
})

describe('所有映射的 i18n key 在 zh/en locale 中均存在', () => {
  // unknown 是 imageErrorMessage 的回退文案，必须在 locale 中存在
  const requiredKeys = Array.from(new Set(Object.values(IMAGE_ERROR_CODE_TO_I18N_KEY)))
    .concat(['unknown'])

  for (const key of requiredKeys) {
    it(`zh locale 包含 aiImage.workspace.errors.${key}`, () => {
      const errors = getErrorsTable(zhDashboard)
      expect(errors).toHaveProperty(key)
      expect(typeof errors[key]).toBe('string')
      expect((errors[key] as string).length).toBeGreaterThan(0)
    })

    it(`en locale 包含 aiImage.workspace.errors.${key}`, () => {
      const errors = getErrorsTable(enDashboard)
      expect(errors).toHaveProperty(key)
      expect(typeof errors[key]).toBe('string')
      expect((errors[key] as string).length).toBeGreaterThan(0)
    })
  }
})
