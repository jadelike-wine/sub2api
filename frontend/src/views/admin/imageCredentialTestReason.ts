// 管理后台凭据测试结果的结构化 reason 取值。
// 与后端 backend/internal/service/image_credential_service.go 中 TestCredentialResult.Reason 保持一致。
export type ImageCredentialTestReason =
  | 'success'
  | 'decrypt_failed'
  | 'auth_failed'
  | 'forbidden'
  | 'rate_limited'
  | 'timeout'
  | 'upstream_error'

// reason → i18n key 后缀映射（命名空间前缀：admin.imageCredentials.test.reason*）。
// 提取到模块顶层以便单元测试覆盖（项目约定：前端工具函数需提取到模块顶层）。
export const IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY: Record<ImageCredentialTestReason, string> = {
  success: 'reasonSuccess',
  decrypt_failed: 'reasonDecryptFailed',
  auth_failed: 'reasonAuthFailed',
  forbidden: 'reasonForbidden',
  rate_limited: 'reasonRateLimited',
  timeout: 'reasonTimeout',
  upstream_error: 'reasonUpstreamError',
}

// i18n 命名空间前缀。
export const IMAGE_CREDENTIAL_TEST_REASON_NAMESPACE = 'admin.imageCredentials.test'

// getImageCredentialTestReasonI18nKey 根据 reason 返回对应的 i18n key（不含命名空间前缀）。
// 未知值返回 null，调用方应回退到原始 reason 字符串或占位符。
export function getImageCredentialTestReasonI18nKey(
  reason?: string | null,
): string | null {
  if (!reason) return null
  return IMAGE_CREDENTIAL_TEST_REASON_I18N_KEY[reason as ImageCredentialTestReason] ?? null
}
