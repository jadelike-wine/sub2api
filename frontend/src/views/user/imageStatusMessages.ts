// generation status → i18n key 映射，用于将后端 status 转换为用户友好的展示文案。
//
// 后端 image_generation status enum 见 backend/ent/schema/image_generation.go：
//   pending / queued / processing / succeeded / failed / canceled
//
// `completed` 不在后端 enum 中（仅 OpenAI Responses API 使用），
// 但作为 `succeeded` 的别名保留映射，防御性处理历史数据或未来字段重命名。
//
// 提取到模块顶层以便单元测试覆盖（项目约定：前端工具函数需提取到模块顶层）。
export const GENERATION_STATUS_TO_I18N_KEY: Record<string, string> = {
  queued: 'queued',
  pending: 'pending',
  processing: 'processing',
  succeeded: 'completed',
  completed: 'completed',
  failed: 'failed',
  canceled: 'canceled',
  // timeout 是前端虚拟状态：当轮询超过 POLL_MAX_DURATION_MS 或连续失败达到
  // POLL_MAX_FAILURES 时由 store 写入，不来自后端。
  timeout: 'timeout',
}

// i18n 命名空间前缀，状态文案统一放在 aiImage.workspace.status.* 下。
export const IMAGE_STATUS_I18N_NAMESPACE = 'aiImage.workspace.status'

// IMAGE_STATUS_UNKNOWN_KEY 是未知状态的兜底 i18n key（不含命名空间前缀）。
// 当 status 不在映射表中时使用，避免直接拼接 key 导致前端显示原始 key 字符串。
export const IMAGE_STATUS_UNKNOWN_KEY = 'unknown'

/**
 * 根据后端 status 返回对应的完整 i18n key。
 * 未匹配时返回兜底 key（aiImage.workspace.status.unknown），
 * 调用方永远不会收到原始 status 拼接的 key。
 */
export function getImageStatusI18nKey(status?: string | null): string {
  if (!status) return `${IMAGE_STATUS_I18N_NAMESPACE}.${IMAGE_STATUS_UNKNOWN_KEY}`
  const suffix = GENERATION_STATUS_TO_I18N_KEY[status] ?? IMAGE_STATUS_UNKNOWN_KEY
  return `${IMAGE_STATUS_I18N_NAMESPACE}.${suffix}`
}
