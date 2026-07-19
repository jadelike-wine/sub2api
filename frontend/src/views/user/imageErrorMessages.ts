// errorCode → i18n key 映射，用于将后端 error_code 转换为用户友好的提示文案。
// 后端错误码定义见 backend/internal/service/image_generation_errors.go。
// 提取到模块顶层以便单元测试覆盖（项目约定：前端工具函数需提取到模块顶层）。
export const IMAGE_ERROR_CODE_TO_I18N_KEY: Record<string, string> = {
  IMAGE_INSUFFICIENT_BALANCE: 'insufficientBalance',
  IMAGE_CONCURRENT_LIMIT: 'concurrentLimit',
  IMAGE_PROVIDER_ERROR: 'upstream',
  IMAGE_PROVIDER_TIMEOUT: 'providerTimeout',
  IMAGE_PROVIDER_RATE_LIMITED: 'providerRateLimited',
  IMAGE_PROVIDER_AUTH_FAILED: 'providerAuthFailed',
  IMAGE_CREDENTIAL_DECRYPT_FAILED: 'credentialDecryptFailed',
  IMAGE_DOWNLOAD_FAILED: 'downloadFailed',
  IMAGE_STORAGE_FAILED: 'storageFailed',
  IMAGE_INVALID_REQUEST: 'invalidRequest',
  IMAGE_PROMPT_REQUIRED: 'promptRequired',
  IMAGE_INVALID_SIZE: 'invalidSize',
  IMAGE_INVALID_RATIO: 'invalidRatio',
  IMAGE_INPUT_REQUIRED: 'inputRequired',
  IMAGE_INPUT_TOO_LARGE: 'inputTooLarge',
  IMAGE_INPUT_UNSUPPORTED: 'inputUnsupported',
  IMAGE_NO_AVAILABLE_CREDENTIAL: 'noCredential',
  IMAGE_QUOTA_EXCEEDED: 'quotaExceeded',
  IMAGE_TASK_ALREADY_RUNNING: 'taskAlreadyRunning',
  IMAGE_GENERATION_DISABLED: 'disabled',
  IMAGE_ACCESS_DENIED: 'forbidden',
  IMAGE_GENERATION_NOT_FOUND: 'notFound',
  IMAGE_CONVERSATION_NOT_FOUND: 'notFound',
  IMAGE_ASSET_NOT_FOUND: 'notFound',
}

// i18n 命名空间前缀，错误文案统一放在 aiImage.workspace.errors.* 下。
export const IMAGE_ERROR_I18N_NAMESPACE = 'aiImage.workspace.errors'

// getImageErrorI18nKey 根据后端 error_code 返回对应的 i18n key（不含命名空间前缀）。
// 未匹配时返回 null，调用方应回退到 unknown 文案。
export function getImageErrorI18nKey(errorCode?: string | null): string | null {
  if (!errorCode) return null
  return IMAGE_ERROR_CODE_TO_I18N_KEY[errorCode] ?? null
}
