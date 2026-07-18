/**
 * Admin AI Image Generation API
 * Manages Agnes provider credentials and queries S3 storage status.
 *
 * 安全：所有接口由 adminAuth 中间件强制管理员鉴权。
 * 凭据响应使用 service.CredentialDTO（已脱敏：不含加密密文、明文 API Key）。
 */

import { apiClient } from '../client'
import type {
  CreateImageCredentialRequest,
  ImageAssetCleanupPreview,
  ImageAssetCleanupRequest,
  ImageAssetCleanupResult,
  ImageGenerationConfig,
  ImagePriceConfig,
  ImageProviderCredential,
  ImageStorageStatus,
  TestImageCredentialResult,
  UpdateImageCredentialRequest,
  UpdateImageGenerationConfigRequest,
} from '@/types'

// ==================== Credentials ====================

/**
 * List all Agnes provider credentials (admin view, desensitized).
 */
export async function listCredentials(
  options?: { signal?: AbortSignal }
): Promise<ImageProviderCredential[]> {
  const { data } = await apiClient.get<ImageProviderCredential[]>(
    '/admin/image-provider-credentials',
    { signal: options?.signal }
  )
  return data
}

/**
 * Get a single credential by ID.
 */
export async function getCredential(id: number): Promise<ImageProviderCredential> {
  const { data } = await apiClient.get<ImageProviderCredential>(
    `/admin/image-provider-credentials/${id}`
  )
  return data
}

/**
 * Create a new credential (api_key is encrypted at rest by the backend).
 */
export async function createCredential(
  payload: CreateImageCredentialRequest
): Promise<ImageProviderCredential> {
  const { data } = await apiClient.post<ImageProviderCredential>(
    '/admin/image-provider-credentials',
    payload
  )
  return data
}

/**
 * Update an existing credential.
 *
 * Notes:
 *  - `api_key` omitted/empty => backend keeps the existing key
 *  - `enabled` / `priority` / `weight` are pointers on the backend; omitting
 *    a field leaves it unchanged
 */
export async function updateCredential(
  id: number,
  payload: UpdateImageCredentialRequest
): Promise<ImageProviderCredential> {
  const { data } = await apiClient.patch<ImageProviderCredential>(
    `/admin/image-provider-credentials/${id}`,
    payload
  )
  return data
}

/**
 * Delete a credential. Historical generations keep their provider_credential_id
 * reference (the credential is removed, not soft-deleted for traceability).
 */
export async function deleteCredential(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(
    `/admin/image-provider-credentials/${id}`
  )
  return data
}

/**
 * Test a credential by issuing a minimal Agnes request.
 * Returns desensitized result (no raw API key in the response).
 */
export async function testCredential(
  id: number
): Promise<TestImageCredentialResult> {
  const { data } = await apiClient.post<TestImageCredentialResult>(
    `/admin/image-provider-credentials/${id}/test`
  )
  return data
}

// ==================== Storage Status ====================

/**
 * Get S3 storage configuration status (does not return AWS Secret).
 */
export async function getStorageStatus(): Promise<ImageStorageStatus> {
  const { data } = await apiClient.get<ImageStorageStatus>(
    '/admin/image-storage/status'
  )
  return data
}

// ==================== Asset Cleanup ====================

/**
 * 预览将要清理的资产数量（不执行删除）。
 * GET /admin/image-storage/cleanup/preview
 */
export async function previewAssetCleanup(
  params: ImageAssetCleanupRequest,
  options?: { signal?: AbortSignal }
): Promise<ImageAssetCleanupPreview> {
  const query: Record<string, string> = {}
  if (params.older_than_days !== undefined && params.older_than_days !== null) {
    query.older_than_days = String(params.older_than_days)
  }
  if (params.before_date) {
    query.before_date = params.before_date
  }
  const { data } = await apiClient.get<ImageAssetCleanupPreview>(
    '/admin/image-storage/cleanup/preview',
    { params: query, signal: options?.signal }
  )
  return data
}

/**
 * 一键清理已软删除的孤立图片资产。
 * POST /admin/image-storage/cleanup
 *
 * 后端流程：storage.Delete(S3Key) → HardDelete(DB 记录)。
 * 容错：单个资产清理失败不阻塞整体，返回统计中包含 failures 计数。
 */
export async function cleanupAssets(
  payload: ImageAssetCleanupRequest
): Promise<ImageAssetCleanupResult> {
  const { data } = await apiClient.post<ImageAssetCleanupResult>(
    '/admin/image-storage/cleanup',
    payload
  )
  return data
}

// ==================== Aggregated Export ====================

export const adminImageGenerationAPI = {
  // Credentials
  listCredentials,
  getCredential,
  createCredential,
  updateCredential,
  deleteCredential,
  testCredential,
  // Storage
  getStorageStatus,
  // Asset Cleanup
  previewAssetCleanup,
  cleanupAssets,
  // Pricing
  getImagePricing,
  updateImagePricing,
  // Generation Config
  getGenerationConfig,
  updateGenerationConfig,
}

export default adminImageGenerationAPI

// ==================== Image Pricing ====================

/**
 * 读取 AI 生图分层价格配置（1K/2K/3K/4K）。
 * 字段为 null 表示该 tier 未配置，将使用 config.yaml 默认值。
 * GET /admin/image-pricing
 */
export async function getImagePricing(): Promise<ImagePriceConfig> {
  const { data } = await apiClient.get<ImagePriceConfig>('/admin/image-pricing')
  return data
}

/**
 * 更新 AI 生图分层价格配置（patch 语义：未传字段保留原值）。
 * PUT /admin/image-pricing
 */
export async function updateImagePricing(payload: ImagePriceConfig): Promise<ImagePriceConfig> {
  const { data } = await apiClient.put<ImagePriceConfig>('/admin/image-pricing', payload)
  return data
}

// ==================== Generation Config ====================

/**
 * 读取当前 AI 生图并发配置（每用户最大并发数）。
 * GET /admin/image-generation-config
 */
export async function getGenerationConfig(): Promise<ImageGenerationConfig> {
  const { data } = await apiClient.get<ImageGenerationConfig>(
    '/admin/image-generation-config'
  )
  return data
}

/**
 * 更新 AI 生图并发配置（修改后立即对新请求生效）。
 * PUT /admin/image-generation-config
 */
export async function updateGenerationConfig(
  payload: UpdateImageGenerationConfigRequest
): Promise<ImageGenerationConfig> {
  const { data } = await apiClient.put<ImageGenerationConfig>(
    '/admin/image-generation-config',
    payload
  )
  return data
}
