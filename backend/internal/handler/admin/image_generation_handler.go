// Package admin provides admin-only HTTP handlers.
package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ImageGenerationHandler 管理员侧图片生成相关 handler（Agnes 凭据 CRUD + S3 配置 + 测试 + 资产清理 + 分层价格配置 + 并发配置）。
//
// 安全约束：
//   - 凭据响应使用 service.CredentialDTO（脱敏：不含加密密文、明文 API Key）
//   - 测试接口只返回脱敏信息（成功/HTTP 状态/耗时/错误码/错误信息/Key 指纹）
//   - 路由注册时由 adminAuth 中间件强制管理员鉴权
type ImageGenerationHandler struct {
	credentialService *service.ImageCredentialService
	storage           service.ImageObjectStorage
	cleanupService    *service.ImageAssetCleanupService
	settingService    *service.SettingService
	cfg               *config.Config
}

// NewImageGenerationHandler 构造管理员侧 handler。
func NewImageGenerationHandler(
	credentialService *service.ImageCredentialService,
	storage service.ImageObjectStorage,
	cleanupService *service.ImageAssetCleanupService,
	settingService *service.SettingService,
	cfg *config.Config,
) *ImageGenerationHandler {
	return &ImageGenerationHandler{
		credentialService: credentialService,
		storage:           storage,
		cleanupService:    cleanupService,
		settingService:    settingService,
		cfg:               cfg,
	}
}

// ==================== 凭据 CRUD ====================

// ListCredentials 列出所有 Agnes 凭据（管理员视图）。
// GET /api/v1/admin/image-provider-credentials
func (h *ImageGenerationHandler) ListCredentials(c *gin.Context) {
	creds, err := h.credentialService.ListCredentials(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, creds)
}

// GetCredential 获取单个凭据详情。
// GET /api/v1/admin/image-provider-credentials/:id
func (h *ImageGenerationHandler) GetCredential(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid credential ID")
		return
	}

	cred, err := h.credentialService.GetCredential(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cred)
}

// CreateCredential 创建凭据（加密存储 API Key）。
// POST /api/v1/admin/image-provider-credentials
func (h *ImageGenerationHandler) CreateCredential(c *gin.Context) {
	var req service.CreateCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	cred, err := h.credentialService.CreateCredential(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cred)
}

// UpdateCredential 更新凭据。
// PATCH /api/v1/admin/image-provider-credentials/:id
func (h *ImageGenerationHandler) UpdateCredential(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid credential ID")
		return
	}

	var req service.UpdateCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	cred, err := h.credentialService.UpdateCredential(c.Request.Context(), id, req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cred)
}

// DeleteCredential 删除凭据（历史生成记录中的 provider_credential_id 保留引用）。
// DELETE /api/v1/admin/image-provider-credentials/:id
func (h *ImageGenerationHandler) DeleteCredential(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid credential ID")
		return
	}

	if err := h.credentialService.DeleteCredential(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "credential deleted"})
}

// TestCredential 测试凭据是否可用（发起最小化 Agnes 请求，只返回脱敏结果）。
// POST /api/v1/admin/image-provider-credentials/:id/test
func (h *ImageGenerationHandler) TestCredential(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid credential ID")
		return
	}

	result, err := h.credentialService.TestCredential(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ==================== S3 配置状态 ====================

// GetStorageStatus 返回存储配置状态（不返回 AWS Secret 或签名密钥）。
// GET /api/v1/admin/image-storage/status
//
// 兼容字段：configured / bucket（local 模式下 bucket 返回 "local"）。
// 新增字段：driver（"s3" 或 "local"）。
func (h *ImageGenerationHandler) GetStorageStatus(c *gin.Context) {
	response.Success(c, gin.H{
		"driver":     h.storage.Driver(),
		"configured": h.storage.Configured(),
		"bucket":     h.storage.Bucket(),
	})
}

// ==================== 资产清理 ====================
//
// 清理"已软删除但存储对象仍存在"的孤立图片资产。
// 支持两种筛选方式（二选一）：
//   - older_than_days: 清理 N 天前软删除的资产
//   - before_date:     清理某日期之前软删除的资产（RFC3339）
// 两者都不传时使用后端配置的 retention_days。

// cleanupAssetsRequest 一键清理请求体。
type cleanupAssetsRequest struct {
	OlderThanDays *int   `json:"older_than_days"`
	BeforeDate    string `json:"before_date"` // RFC3339，可选
	BatchSize     int    `json:"batch_size"`  // 可选，<=0 走配置默认
}

// CleanupAssets 一键清理已软删除的孤立图片资产。
// POST /api/v1/admin/image-storage/cleanup
//
// 流程：storage.Delete(S3Key) → HardDelete(DB 记录)。
// 容错：单个资产清理失败不阻塞整体，返回统计中包含 failures 计数。
func (h *ImageGenerationHandler) CleanupAssets(c *gin.Context) {
	if h.cleanupService == nil {
		response.BadRequest(c, "cleanup service not configured")
		return
	}
	var req cleanupAssetsRequest
	_ = c.ShouldBindJSON(&req)

	params, err := buildCleanupParams(req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	params.Reason = "manual"

	result, err := h.cleanupService.RunOnce(c.Request.Context(), params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// PreviewCleanup 预览将要清理的资产数量（不执行删除）。
// GET /api/v1/admin/image-storage/cleanup/preview?older_than_days=7&before_date=2026-01-01T00:00:00Z
func (h *ImageGenerationHandler) PreviewCleanup(c *gin.Context) {
	if h.cleanupService == nil {
		response.BadRequest(c, "cleanup service not configured")
		return
	}
	req := cleanupAssetsRequest{
		OlderThanDays: parseOptionalInt(c.Query("older_than_days")),
		BeforeDate:    c.Query("before_date"),
	}
	params, err := buildCleanupParams(req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	count, err := h.cleanupService.PreviewCleanup(c.Request.Context(), params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"count":  count,
		"cutoff": params.BeforeDate,
	})
}

// buildCleanupParams 从请求构造清理参数并校验。
func buildCleanupParams(req cleanupAssetsRequest) (service.CleanupParams, error) {
	params := service.CleanupParams{
		OlderThanDays: req.OlderThanDays,
		BatchSize:     req.BatchSize,
	}
	if req.BeforeDate != "" {
		t, err := time.Parse(time.RFC3339, req.BeforeDate)
		if err != nil {
			return params, errInvalidBeforeDate()
		}
		params.BeforeDate = &t
	}
	if err := service.ValidateCleanupParams(params); err != nil {
		return params, err
	}
	return params, nil
}

// parseOptionalInt 解析可选整数查询参数。
func parseOptionalInt(s string) *int {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

// errInvalidBeforeDate 返回 before_date 格式错误。
func errInvalidBeforeDate() error {
	return &invalidBeforeDateError{}
}

type invalidBeforeDateError struct{}

func (e *invalidBeforeDateError) Error() string { return "before_date must be RFC3339 formatted" }

// ==================== AI 生图分层价格配置 ====================

// imagePriceConfigRequest 是管理员后台配置 AI 生图分层价格的请求体。
// 各字段为指针：null/省略表示"不修改该 tier 的现有配置"。
type imagePriceConfigRequest struct {
	Price1K *float64 `json:"price_1k_usd,omitempty"`
	Price2K *float64 `json:"price_2k_usd,omitempty"`
	Price3K *float64 `json:"price_3k_usd,omitempty"`
	Price4K *float64 `json:"price_4k_usd,omitempty"`
}

// GetImagePricing 读取当前 AI 生图分层价格配置。
// GET /api/v1/admin/image-pricing
// 返回 settings 表中的配置（null 表示该 tier 未配置，将使用 config.yaml 默认值）。
func (h *ImageGenerationHandler) GetImagePricing(c *gin.Context) {
	if h.settingService == nil {
		response.InternalError(c, "setting service not configured")
		return
	}
	cfg, err := h.settingService.GetImagePriceConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 未配置时返回空对象（各字段为 null），前端据此展示"未配置"占位
	if cfg == nil {
		cfg = &service.ImagePriceConfigSetting{}
	}
	response.Success(c, cfg)
}

// UpdateImagePricing 更新 AI 生图分层价格配置。
// PUT /api/v1/admin/image-pricing
// 请求体：imagePriceConfigRequest（任一字段非 null 表示覆盖；null 表示保留原值不动）。
// 由于 settings.Set 是整体写入，未传字段需先读出现有配置合并后再写。
func (h *ImageGenerationHandler) UpdateImagePricing(c *gin.Context) {
	if h.settingService == nil {
		response.InternalError(c, "setting service not configured")
		return
	}
	var req imagePriceConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// 先读现有配置（patch 语义：未传字段保留原值）
	existing, err := h.settingService.GetImagePriceConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if existing == nil {
		existing = &service.ImagePriceConfigSetting{}
	}
	// 字段级 patch
	if req.Price1K != nil {
		existing.Price1K = req.Price1K
	}
	if req.Price2K != nil {
		existing.Price2K = req.Price2K
	}
	if req.Price3K != nil {
		existing.Price3K = req.Price3K
	}
	if req.Price4K != nil {
		existing.Price4K = req.Price4K
	}

	if err := h.settingService.SetImagePriceConfig(c.Request.Context(), existing); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, existing)
}

// ==================== AI 生图并发配置 ====================

// generationConfigResponse 是并发配置的响应体。
// 同时返回 settings 中的覆盖值和 config.yaml 默认值，便于前端展示"当前生效值"。
type generationConfigResponse struct {
	// MaxConcurrentPerUser 当前生效值（settings 覆盖 > config 默认）。
	MaxConcurrentPerUser int `json:"max_concurrent_per_user"`
	// ConfigDefault config.yaml 中的默认值（供前端展示"未配置时回退到 X"）。
	ConfigDefault int `json:"config_default"`
	// Configured 是否已在后台显式配置（true=使用 settings 值，false=使用 config 默认）。
	Configured bool `json:"configured"`
}

// generationConfigRequest 是并发配置的更新请求体。
type generationConfigRequest struct {
	// MaxConcurrentPerUser 必须为正整数（>= 1）。
	MaxConcurrentPerUser *int `json:"max_concurrent_per_user"`
}

// GetGenerationConfig 读取当前 AI 生图并发配置。
// GET /api/v1/admin/image-generation-config
func (h *ImageGenerationHandler) GetGenerationConfig(c *gin.Context) {
	if h.settingService == nil {
		response.InternalError(c, "setting service not configured")
		return
	}
	configDefault := 0
	if h.cfg != nil {
		configDefault = h.cfg.ImageGeneration.MaxConcurrentPerUser
	}

	value, ok := h.settingService.GetImageMaxConcurrentPerUser(c.Request.Context())
	if !ok {
		// 未配置：返回 config 默认值
		response.Success(c, generationConfigResponse{
			MaxConcurrentPerUser: configDefault,
			ConfigDefault:        configDefault,
			Configured:           false,
		})
		return
	}
	response.Success(c, generationConfigResponse{
		MaxConcurrentPerUser: value,
		ConfigDefault:        configDefault,
		Configured:           true,
	})
}

// UpdateGenerationConfig 更新 AI 生图并发配置。
// PUT /api/v1/admin/image-generation-config
// 修改后立即对新请求生效（每次 CreateGeneration 都读取 settings）。
func (h *ImageGenerationHandler) UpdateGenerationConfig(c *gin.Context) {
	if h.settingService == nil {
		response.InternalError(c, "setting service not configured")
		return
	}
	var req generationConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.MaxConcurrentPerUser == nil {
		response.BadRequest(c, "max_concurrent_per_user is required")
		return
	}
	if *req.MaxConcurrentPerUser < 1 {
		response.BadRequest(c, "max_concurrent_per_user must be a positive integer (>= 1)")
		return
	}

	if err := h.settingService.SetImageMaxConcurrentPerUser(c.Request.Context(), *req.MaxConcurrentPerUser); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	configDefault := 0
	if h.cfg != nil {
		configDefault = h.cfg.ImageGeneration.MaxConcurrentPerUser
	}
	response.Success(c, generationConfigResponse{
		MaxConcurrentPerUser: *req.MaxConcurrentPerUser,
		ConfigDefault:        configDefault,
		Configured:           true,
	})
}
