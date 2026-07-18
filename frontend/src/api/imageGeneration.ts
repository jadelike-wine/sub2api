/**
 * AI Image Generation API (user side)
 * Handles conversations / generations / asset uploads for the current user.
 *
 * 所有接口走 apiClient 单例，响应拦截器已自动解包 { code, message, data }。
 */

import { apiClient } from './client'
import type {
  CreateImageConversationRequest,
  CreateImageGenerationRequest,
  ImageConversation,
  ImageConversationListParams,
  ImageGeneration,
  ImageGenerationListParams,
  ImageAsset,
  ConfirmUploadRequest,
  PaginatedResponse,
  PresignUploadResponse,
  RefreshAssetURLResponse,
  UpdateImageConversationRequest,
} from '@/types'

// ==================== Conversations ====================

/**
 * List image conversations for the current user.
 * @param params - Pagination / keyword filters
 * @param options.signal - Optional AbortSignal for cancellation
 */
export async function listConversations(
  params?: ImageConversationListParams,
  options?: { signal?: AbortSignal }
): Promise<PaginatedResponse<ImageConversation>> {
  const { data } = await apiClient.get<PaginatedResponse<ImageConversation>>(
    '/image-conversations',
    { params, signal: options?.signal }
  )
  return data
}

/**
 * Get a single conversation by ID.
 */
export async function getConversation(id: number): Promise<ImageConversation> {
  const { data } = await apiClient.get<ImageConversation>(`/image-conversations/${id}`)
  return data
}

/**
 * Create a new conversation. Title is optional (server may auto-generate).
 */
export async function createConversation(
  payload: CreateImageConversationRequest = {}
): Promise<ImageConversation> {
  const { data } = await apiClient.post<ImageConversation>('/image-conversations', payload)
  return data
}

/**
 * Update a conversation (currently only the title).
 */
export async function updateConversation(
  id: number,
  payload: UpdateImageConversationRequest
): Promise<ImageConversation> {
  const { data } = await apiClient.patch<ImageConversation>(`/image-conversations/${id}`, payload)
  return data
}

/**
 * Delete a conversation. Server cascades generations/assets under it.
 */
export async function deleteConversation(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/image-conversations/${id}`)
  return data
}

/**
 * List generations that belong to a specific conversation.
 * 后端返回的是数组（非分页结构），故此处类型为 ImageGeneration[]。
 */
export async function listGenerationsByConversation(
  conversationId: number,
  params?: { page?: number; page_size?: number },
  options?: { signal?: AbortSignal }
): Promise<ImageGeneration[]> {
  const { data } = await apiClient.get<ImageGeneration[]>(
    `/image-conversations/${conversationId}/generations`,
    { params, signal: options?.signal }
  )
  return data ?? []
}

// ==================== Generations ====================

/**
 * Create a new generation task. Returns immediately with a pending generation.
 *
 * 幂等：调用方可通过 `idempotencyKey` 传入 Idempotency-Key（同一 key 重复调用
 * 在服务端会返回同一 generation，避免重复扣费）。
 *
 * @param payload - Generation request body
 * @param idempotencyKey - Optional idempotency key (sent via header)
 */
export async function createGeneration(
  payload: CreateImageGenerationRequest,
  idempotencyKey?: string
): Promise<ImageGeneration> {
  const headers: Record<string, string> = {}
  if (idempotencyKey) {
    headers['Idempotency-Key'] = idempotencyKey
  }
  const { data } = await apiClient.post<ImageGeneration>('/image-generations', payload, {
    headers,
  })
  return data
}

/**
 * List generations for the current user (optionally filtered by status/conversation).
 */
export async function listGenerations(
  params?: ImageGenerationListParams,
  options?: { signal?: AbortSignal }
): Promise<PaginatedResponse<ImageGeneration>> {
  const { data } = await apiClient.get<PaginatedResponse<ImageGeneration>>(
    '/image-generations',
    { params, signal: options?.signal }
  )
  return data
}

/**
 * Get a single generation by ID (includes output_assets with presigned URLs).
 */
export async function getGeneration(id: number): Promise<ImageGeneration> {
  const { data } = await apiClient.get<ImageGeneration>(`/image-generations/${id}`)
  return data
}

/**
 * Delete a generation (also removes its assets on S3).
 */
export async function deleteGeneration(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/image-generations/${id}`)
  return data
}

/**
 * List all assets (input + output) for a generation.
 * Output assets include short-lived presigned GET URLs.
 */
export async function getGenerationAssets(
  id: number,
  options?: { signal?: AbortSignal }
): Promise<ImageAsset[]> {
  const { data } = await apiClient.get<ImageAsset[]>(`/image-generations/${id}/assets`, {
    signal: options?.signal,
  })
  return data
}

// ==================== Asset Upload ====================

/**
 * Request a presigned PUT URL to upload an image directly to S3.
 * @param mimeType - Must be one of IMAGE_GENERATION_INPUT_MIME_TYPES
 */
export async function presignUpload(
  mimeType: string
): Promise<PresignUploadResponse> {
  const { data } = await apiClient.post<PresignUploadResponse>('/image-assets/presign-upload', {
    mime_type: mimeType,
  })
  return data
}

/**
 * Upload a binary file directly to the presigned S3 PUT URL.
 *
 * This bypasses apiClient (the presigned URL points at S3, not the backend),
 * so we use a plain fetch with the appropriate content-type.
 *
 * @param uploadUrl - Presigned PUT URL from presignUpload()
 * @param file - Binary file to upload
 * @param mimeType - MIME type matching the presign request
 */
export async function uploadToPresignedUrl(
  uploadUrl: string,
  file: Blob | File,
  mimeType: string
): Promise<void> {
  const resp = await fetch(uploadUrl, {
    method: 'PUT',
    headers: { 'Content-Type': mimeType },
    body: file,
  })
  if (!resp.ok) {
    throw new Error(`S3 upload failed: ${resp.status} ${resp.statusText}`)
  }
}

/**
 * Confirm an S3 upload and create the asset record on the backend.
 *
 * Must be called after uploadToPresignedUrl() succeeds; the backend will
 * HEAD the object to verify its existence and size.
 */
export async function confirmUpload(
  payload: ConfirmUploadRequest
): Promise<ImageAsset> {
  const { data } = await apiClient.post<ImageAsset>('/image-assets/confirm-upload', payload)
  return data
}

/**
 * Refresh the presigned GET URL for an asset (URLs expire after ~30min).
 */
export async function refreshAssetURL(
  assetId: number
): Promise<RefreshAssetURLResponse> {
  const { data } = await apiClient.post<RefreshAssetURLResponse>(
    `/image-assets/${assetId}/refresh-url`
  )
  return data
}

// ==================== Aggregated Export ====================

export const imageGenerationAPI = {
  // Conversations
  listConversations,
  getConversation,
  createConversation,
  updateConversation,
  deleteConversation,
  listGenerationsByConversation,
  // Generations
  createGeneration,
  listGenerations,
  getGeneration,
  deleteGeneration,
  getGenerationAssets,
  // Asset upload
  presignUpload,
  uploadToPresignedUrl,
  confirmUpload,
  refreshAssetURL,
}

export default imageGenerationAPI
