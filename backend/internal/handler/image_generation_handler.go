// Package handler provides HTTP request handlers for the application.
package handler

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ImageGenerationHandler 处理用户侧图片生成相关请求（会话/生成任务/资产上传）。
//
// 安全约束：
//   - 所有资源查询通过 service 层强制附带 user_id 条件
//   - 不向上层返回 provider_credential_id（管理员才可见）
//   - 不返回加密密文、API Key 明文、AWS Secret
//   - 上游错误已由 service 层脱敏
type ImageGenerationHandler struct {
	genService    *service.ImageGenerationService
	assetService  *service.ImageAssetService
	presignExpiry time.Duration
}

// NewImageGenerationHandler 构造用户侧图片生成 handler。
func NewImageGenerationHandler(
	genService *service.ImageGenerationService,
	assetService *service.ImageAssetService,
) *ImageGenerationHandler {
	return &ImageGenerationHandler{
		genService:    genService,
		assetService:  assetService,
		presignExpiry: 30 * time.Minute, // 前端访问 URL 默认 30 分钟
	}
}

// ==================== 会话 ====================

// CreateConversationRequest 创建会话请求。
type CreateConversationRequest struct {
	Title string `json:"title"`
}

// UpdateConversationRequest 更新会话请求。
type UpdateConversationRequest struct {
	Title string `json:"title" binding:"required"`
}

// CreateConversation 创建用户的图片生成会话。
// POST /api/v1/image-conversations
func (h *ImageGenerationHandler) CreateConversation(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req CreateConversationRequest
	_ = c.ShouldBindJSON(&req)

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "新会话"
	}

	conv, err := h.genService.CreateConversation(c.Request.Context(), subject.UserID, title)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, conversationToDTO(conv))
}

// ListConversations 列出用户的会话（分页 + 关键词搜索）。
// GET /api/v1/image-conversations
func (h *ImageGenerationHandler) ListConversations(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	page, pageSize := response.ParsePagination(c)
	filter := service.ImageConversationFilter{
		UserID:   subject.UserID,
		Keyword:  strings.TrimSpace(c.Query("keyword")),
		Page:     page,
		PageSize: pageSize,
	}
	if v := c.Query("created_after"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.CreatedAfter = &t
		}
	}
	if v := c.Query("created_before"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.CreatedBefore = &t
		}
	}

	list, err := h.genService.ListConversations(c.Request.Context(), subject.UserID, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]*imageConversationDTO, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, conversationToDTO(item))
	}
	response.Paginated(c, out, list.Total, list.Page, list.PageSize)
}

// GetConversation 获取会话详情。
// GET /api/v1/image-conversations/:id
func (h *ImageGenerationHandler) GetConversation(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid conversation ID")
		return
	}

	conv, err := h.genService.GetConversation(c.Request.Context(), subject.UserID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, conversationToDTO(conv))
}

// UpdateConversation 更新会话标题。
// PATCH /api/v1/image-conversations/:id
func (h *ImageGenerationHandler) UpdateConversation(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid conversation ID")
		return
	}

	var req UpdateConversationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	conv, err := h.genService.UpdateConversation(c.Request.Context(), subject.UserID, id, req.Title)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, conversationToDTO(conv))
}

// DeleteConversation 软删除会话及其下所有生成任务和资产。
// DELETE /api/v1/image-conversations/:id
func (h *ImageGenerationHandler) DeleteConversation(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid conversation ID")
		return
	}

	if err := h.genService.DeleteConversation(c.Request.Context(), subject.UserID, id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "conversation deleted"})
}

// ==================== 生成任务 ====================

// CreateGenerationRequest 创建生成任务请求。
type CreateGenerationRequest struct {
	ConversationID     *int64  `json:"conversation_id"`
	ParentGenerationID *int64  `json:"parent_generation_id"`
	Type               string  `json:"type" binding:"required,oneof=text_to_image image_to_image"`
	Prompt             string  `json:"prompt" binding:"required"`
	Size               string  `json:"size" binding:"required"`
	Ratio              string  `json:"ratio"`
	InputAssetIDs      []int64 `json:"input_asset_ids"`
}

// CreateGeneration 创建生成任务（立即返回 pending 状态的 generation_id）。
// 幂等键通过 Idempotency-Key header 传入，service 层负责幂等校验。
// POST /api/v1/image-generations
func (h *ImageGenerationHandler) CreateGeneration(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req CreateGenerationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	svcReq := service.CreateGenerationRequest{
		ConversationID:     req.ConversationID,
		ParentGenerationID: req.ParentGenerationID,
		Type:               req.Type,
		Prompt:             req.Prompt,
		Size:               req.Size,
		Ratio:              req.Ratio,
		InputAssetIDs:      req.InputAssetIDs,
		IdempotencyKey:     strings.TrimSpace(c.GetHeader("Idempotency-Key")),
	}

	gen, err := h.genService.CreateGeneration(c.Request.Context(), subject.UserID, svcReq)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, generationToDTO(gen))
}

// GetGeneration 获取生成任务详情（附带 user_id 隔离）。
// GET /api/v1/image-generations/:id
func (h *ImageGenerationHandler) GetGeneration(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid generation ID")
		return
	}

	gen, err := h.genService.GetGeneration(c.Request.Context(), subject.UserID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	dto := generationToDTO(gen)
	// 附带资产访问 URL（input + output 分别填充，否则前端拿到 succeeded 状态但无图片）
	allAssets := h.attachAssetURLs(c, subject.UserID, id)
	dto.InputAssets, dto.OutputAssets = splitAssetsByType(allAssets)
	response.Success(c, dto)
}

// ListGenerationsByConversation 列出会话下的所有生成任务。
// GET /api/v1/image-conversations/:id/generations
func (h *ImageGenerationHandler) ListGenerationsByConversation(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid conversation ID")
		return
	}

	gens, err := h.genService.ListGenerationsByConversation(c.Request.Context(), subject.UserID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]*imageGenerationDTO, 0, len(gens))
	for _, g := range gens {
		out = append(out, generationToDTO(g))
	}
	// 同时附带每个 generation 的资产访问 URL（input + output）
	for i, g := range gens {
		allAssets := h.attachAssetURLs(c, subject.UserID, g.ID)
		out[i].InputAssets, out[i].OutputAssets = splitAssetsByType(allAssets)
	}
	response.Success(c, out)
}

// ListGenerations 列出用户的生成任务（分页）。
// GET /api/v1/image-generations
func (h *ImageGenerationHandler) ListGenerations(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	page, pageSize := response.ParsePagination(c)
	filter := service.ImageGenerationFilter{
		UserID:   subject.UserID,
		Status:   c.Query("status"),
		Page:     page,
		PageSize: pageSize,
	}
	if v := c.Query("conversation_id"); v != "" {
		if cid, err := strconv.ParseInt(v, 10, 64); err == nil {
			filter.ConversationID = &cid
		}
	}

	list, err := h.genService.ListGenerations(c.Request.Context(), subject.UserID, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]*imageGenerationDTO, 0, len(list.Items))
	for _, g := range list.Items {
		dto := generationToDTO(g)
		allAssets := h.attachAssetURLs(c, subject.UserID, g.ID)
		dto.InputAssets, dto.OutputAssets = splitAssetsByType(allAssets)
		out = append(out, dto)
	}
	response.Paginated(c, out, list.Total, list.Page, list.PageSize)
}

// DeleteGeneration 软删除生成任务及其资产。
// DELETE /api/v1/image-generations/:id
func (h *ImageGenerationHandler) DeleteGeneration(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid generation ID")
		return
	}

	if err := h.genService.DeleteGeneration(c.Request.Context(), subject.UserID, id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "generation deleted"})
}

// GetGenerationAssets 获取某次生成任务的所有资产及其访问 URL（输入+输出）。
// GET /api/v1/image-generations/:id/assets
func (h *ImageGenerationHandler) GetGenerationAssets(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid generation ID")
		return
	}

	out := h.attachAssetURLs(c, subject.UserID, id)
	response.Success(c, out)
}

// RefreshAssetURL 刷新单个资产的访问 URL（Presigned URL 过期后前端可调用）。
// POST /api/v1/image-assets/:id/refresh-url
func (h *ImageGenerationHandler) RefreshAssetURL(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid asset ID")
		return
	}

	url, err := h.assetService.GetAssetAccessURL(c.Request.Context(), subject.UserID, id, h.presignExpiry)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"url": url, "expires_in": int(h.presignExpiry.Seconds())})
}

// ==================== 资产上传 ====================

// PresignUploadRequest 申请直传 S3 的预签名 PUT URL。
type PresignUploadRequest struct {
	MimeType string `json:"mime_type" binding:"required"`
}

// PresignUpload 生成用户直传 S3 的预签名 PUT URL。
// POST /api/v1/image-assets/presign-upload
func (h *ImageGenerationHandler) PresignUpload(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req PresignUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	resp, err := h.assetService.PresignUpload(c.Request.Context(), subject.UserID, service.PresignUploadRequest{
		MimeType: req.MimeType,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"upload_url": resp.UploadURL,
		"s3_key":     resp.S3Key,
		"expires_in": resp.ExpiresIn,
	})
}

// ConfirmUploadRequest 确认上传请求。
type ConfirmUploadRequest struct {
	S3Key            string  `json:"s3_key" binding:"required"`
	MimeType         string  `json:"mime_type"`
	OriginalFilename *string `json:"original_filename"`
}

// ConfirmUpload 校验用户上传的 S3 对象并创建资产记录。
// POST /api/v1/image-assets/confirm-upload
func (h *ImageGenerationHandler) ConfirmUpload(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req ConfirmUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	asset, err := h.assetService.ConfirmUpload(c.Request.Context(), subject.UserID, service.ConfirmUploadRequest{
		S3Key:            req.S3Key,
		MimeType:         req.MimeType,
		OriginalFilename: req.OriginalFilename,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 生成访问 URL 一并返回，否则前端拿到 url="" 无法预览
	accessURL, _ := h.assetService.GetAssetAccessURL(c.Request.Context(), subject.UserID, asset.ID, h.presignExpiry)
	response.Success(c, assetToDTO(asset, accessURL))
}

// ==================== DTO ====================

// imageConversationDTO 会话 DTO（不含敏感字段）。
type imageConversationDTO struct {
	ID            int64      `json:"id"`
	Title         string     `json:"title"`
	LastMessageAt *time.Time `json:"last_message_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func conversationToDTO(c *service.ImageConversation) *imageConversationDTO {
	if c == nil {
		return nil
	}
	return &imageConversationDTO{
		ID:            c.ID,
		Title:         c.Title,
		LastMessageAt: c.LastMessageAt,
		CreatedAt:     c.CreatedAt,
		UpdatedAt:     c.UpdatedAt,
	}
}

// imageGenerationDTO 生成任务 DTO。
// 安全：不返回 provider_credential_id（非管理员不可见）；
//       不返回 model（C 端用户无需感知具体上游模型）。
type imageGenerationDTO struct {
	ID                  int64      `json:"id"`
	ConversationID      int64      `json:"conversation_id"`
	ParentGenerationID  *int64     `json:"parent_generation_id,omitempty"`
	Provider            string     `json:"provider"`
	GenerationType      string     `json:"generation_type"`
	Prompt              string     `json:"prompt"`
	Size                string     `json:"size"`
	Ratio               string     `json:"ratio"`
	Status              string     `json:"status"`
	ErrorCode           *string    `json:"error_code,omitempty"`
	ErrorMessage        *string    `json:"error_message,omitempty"`
	DurationMs          int        `json:"duration_ms"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	InputAssets         []*imageAssetDTO `json:"input_assets,omitempty"`
	OutputAssets        []*imageAssetDTO `json:"output_assets,omitempty"`
}

func generationToDTO(g *service.ImageGeneration) *imageGenerationDTO {
	if g == nil {
		return nil
	}
	return &imageGenerationDTO{
		ID:                 g.ID,
		ConversationID:     g.ConversationID,
		ParentGenerationID: g.ParentGenerationID,
		Provider:           g.Provider,
		GenerationType:     g.GenerationType,
		Prompt:             g.Prompt,
		Size:               g.Size,
		Ratio:              g.Ratio,
		Status:             g.Status,
		ErrorCode:          g.ErrorCode,
		ErrorMessage:       g.ErrorMessage,
		DurationMs:         g.DurationMs,
		StartedAt:          g.StartedAt,
		CompletedAt:        g.CompletedAt,
		CreatedAt:          g.CreatedAt,
		UpdatedAt:          g.UpdatedAt,
	}
}

// imageAssetDTO 图片资产 DTO。
type imageAssetDTO struct {
	ID               int64      `json:"id"`
	GenerationID     int64      `json:"generation_id"`
	AssetType        string     `json:"asset_type"`
	MimeType         string     `json:"mime_type"`
	FileSize         int64      `json:"file_size"`
	Width            *int       `json:"width,omitempty"`
	Height           *int       `json:"height,omitempty"`
	OriginalFilename *string    `json:"original_filename,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	// URL 短期 Presigned GET URL（可能为空，前端可调用 refresh-url 接口重新获取）
	URL     string `json:"url,omitempty"`
}

func assetToDTO(a *service.ImageAsset, url string) *imageAssetDTO {
	if a == nil {
		return nil
	}
	return &imageAssetDTO{
		ID:               a.ID,
		GenerationID:     a.GenerationID,
		AssetType:        a.AssetType,
		MimeType:         a.MimeType,
		FileSize:         a.FileSize,
		Width:            a.Width,
		Height:           a.Height,
		OriginalFilename: a.OriginalFilename,
		CreatedAt:        a.CreatedAt,
		URL:              url,
	}
}

// attachAssetURLs 获取某次生成任务的所有资产并附上访问 URL。
// 失败时返回空切片（不阻塞主流程）。
func (h *ImageGenerationHandler) attachAssetURLs(c *gin.Context, userID, generationID int64) []*imageAssetDTO {
	assets, err := h.assetService.GetAssetsByGeneration(c.Request.Context(), userID, generationID)
	if err != nil || len(assets) == 0 {
		return []*imageAssetDTO{}
	}
	urls, err := h.assetService.GetAssetAccessURLsByGeneration(c.Request.Context(), userID, generationID, h.presignExpiry)
	if err != nil {
		urls = nil
	}
	out := make([]*imageAssetDTO, 0, len(assets))
	for _, a := range assets {
		var u string
		if urls != nil {
			u = urls[a.ID]
		}
		out = append(out, assetToDTO(a, u))
	}
	return out
}

// splitAssetsByType 按 asset_type 拆分为 input 和 output 两组。
// thumbnail 归入 output（前端按需展示）。
func splitAssetsByType(assets []*imageAssetDTO) (input, output []*imageAssetDTO) {
	for _, a := range assets {
		if a.AssetType == service.ImageAssetTypeInput {
			input = append(input, a)
		} else {
			output = append(output, a)
		}
	}
	return input, output
}
