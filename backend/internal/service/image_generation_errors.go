package service

import (
	"errors"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 图片生成业务错误码。Reason 是稳定错误码，前端基于此做 i18n，
// 不依赖上游英文错误字符串。Message 已脱敏，绝不泄露完整 API Key 或 Authorization Header。
//
// 上游原始错误通过 WithCause 附加，仅用于日志，不返回前端。

// ImageGenError 是 infraerrors.ApplicationError 的语义别名，便于 service 层类型标注。
type ImageGenError = infraerrors.ApplicationError

// ErrImageCredentialNotFound 表示 Agnes 凭据不存在。
// 作为哨兵错误供 repository 层返回，service 层判断。
var ErrImageCredentialNotFound = infraerrors.New(http.StatusNotFound, "IMAGE_CREDENTIAL_NOT_FOUND", "image provider credential not found")

// ErrImageConversationNotFound 已在下方通过 errImageConversationNotFound 函数构造，
// 这里同时提供哨兵形式以便 repository 层使用。
var ErrImageConversationNotFound = infraerrors.New(http.StatusNotFound, "IMAGE_CONVERSATION_NOT_FOUND", "image conversation not found")

// ErrImageGenerationNotFound 哨兵形式，供 repository 层使用。
var ErrImageGenerationNotFound = infraerrors.New(http.StatusNotFound, "IMAGE_GENERATION_NOT_FOUND", "image generation not found")

// ErrImageConcurrentLimitReached 当用户活跃生图任务数达到并发上限时返回。
// 由 repository.CreateIfUnderUserConcurrency 在并发检查失败时返回。
var ErrImageConcurrentLimitReached = infraerrors.New(http.StatusConflict, "IMAGE_CONCURRENT_LIMIT", "image generation concurrency limit reached for user")

// ErrAllCredentialsBusy 当所有可用凭据都被占用时返回（区别于"无健康凭据"）。
// 调用方应将任务放入 queued 队列等待调度，而非标记失败。
var ErrAllCredentialsBusy = errors.New("all available credentials are busy")

// ErrImageAssetNotFound 哨兵形式，供 repository 层使用。
var ErrImageAssetNotFound = infraerrors.New(http.StatusNotFound, "IMAGE_ASSET_NOT_FOUND", "image asset not found")

func errImageInvalidRequest(msg string) *ImageGenError {
	return infraerrors.New(http.StatusBadRequest, "IMAGE_INVALID_REQUEST", msg)
}

func errImagePromptRequired() *ImageGenError {
	return infraerrors.New(http.StatusBadRequest, "IMAGE_PROMPT_REQUIRED", "prompt is required")
}

func errImageInvalidSize(v string) *ImageGenError {
	return infraerrors.New(http.StatusBadRequest, "IMAGE_INVALID_SIZE", "invalid size: "+v)
}

func errImageInvalidRatio(v string) *ImageGenError {
	return infraerrors.New(http.StatusBadRequest, "IMAGE_INVALID_RATIO", "invalid ratio: "+v)
}

func errImageInputRequired() *ImageGenError {
	return infraerrors.New(http.StatusBadRequest, "IMAGE_INPUT_REQUIRED", "at least one input image is required for image_to_image")
}

func errImageInputTooLarge() *ImageGenError {
	return infraerrors.New(http.StatusBadRequest, "IMAGE_INPUT_TOO_LARGE", "input image exceeds size limit")
}

func errImageInputUnsupported(mime string) *ImageGenError {
	return infraerrors.New(http.StatusBadRequest, "IMAGE_INPUT_UNSUPPORTED", "unsupported input image type: "+mime)
}

func errImageNoAvailableCredential() *ImageGenError {
	return infraerrors.New(http.StatusServiceUnavailable, "IMAGE_NO_AVAILABLE_CREDENTIAL", "no healthy upstream credential available")
}

func errImageProviderTimeout() *ImageGenError {
	return infraerrors.New(http.StatusGatewayTimeout, "IMAGE_PROVIDER_TIMEOUT", "upstream image generation timed out")
}

func errImageProviderRateLimited() *ImageGenError {
	return infraerrors.New(http.StatusTooManyRequests, "IMAGE_PROVIDER_RATE_LIMITED", "upstream rate limited, please retry later")
}

func errImageProviderAuthFailed() *ImageGenError {
	return infraerrors.New(http.StatusServiceUnavailable, "IMAGE_PROVIDER_AUTH_FAILED", "upstream authentication failed")
}

func errImageProviderError(msg string) *ImageGenError {
	return infraerrors.New(http.StatusServiceUnavailable, "IMAGE_PROVIDER_ERROR", sanitizeUpstreamErrorMessage(msg))
}

func errImageDownloadFailed() *ImageGenError {
	return infraerrors.New(http.StatusServiceUnavailable, "IMAGE_DOWNLOAD_FAILED", "failed to download generated image")
}

func errImageStorageFailed() *ImageGenError {
	return infraerrors.New(http.StatusInternalServerError, "IMAGE_STORAGE_FAILED", "failed to store generated image")
}

func errImageGenerationNotFound() *ImageGenError {
	return infraerrors.New(http.StatusNotFound, "IMAGE_GENERATION_NOT_FOUND", "image generation not found")
}

func errImageConversationNotFound() *ImageGenError {
	return infraerrors.New(http.StatusNotFound, "IMAGE_CONVERSATION_NOT_FOUND", "image conversation not found")
}

func errImageAccessDenied() *ImageGenError {
	return infraerrors.New(http.StatusForbidden, "IMAGE_ACCESS_DENIED", "you do not have access to this resource")
}

func errImageTaskAlreadyRunning() *ImageGenError {
	return infraerrors.New(http.StatusConflict, "IMAGE_TASK_ALREADY_RUNNING", "a generation task is already running in this conversation")
}

func errImageDisabled() *ImageGenError {
	return infraerrors.New(http.StatusForbidden, "IMAGE_GENERATION_DISABLED", "image generation is not enabled")
}

func errImageQuotaExceeded() *ImageGenError {
	return infraerrors.New(http.StatusTooManyRequests, "IMAGE_QUOTA_EXCEEDED", "image generation quota exceeded")
}

func errImageInputKeyNotOwned() *ImageGenError {
	return infraerrors.New(http.StatusForbidden, "IMAGE_ACCESS_DENIED", "input image does not belong to the current user")
}

// errImageInsufficientBalance 余额不足以生成图片（预检失败）。
// HTTP 402 Payment Required，前端可据此引导充值。
func errImageInsufficientBalance() *ImageGenError {
	return infraerrors.New(http.StatusPaymentRequired, "IMAGE_INSUFFICIENT_BALANCE", "insufficient balance to generate image")
}
