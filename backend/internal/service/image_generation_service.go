package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ImageGenerationDisplayModel 是生图功能在用户侧展示的统一模型名。
// 出于安全考虑，不向 C 端用户泄露真实上游模型名（如 agnes-image-2.1-flash），
// 使用记录、模型分布统计等一律展示此常量。上游 API 调用仍使用 cfg.AgnesModel。
const ImageGenerationDisplayModel = "enova-image"

// ImageGenerationService 是图片生成功能的业务编排层。
//
// 职责：
//   - 会话 CRUD（强制 user_id 隔离）
//   - 生成任务创建（幂等校验 + 异步处理）
//   - 异步处理：调度凭据 → 调用 Agnes → 下载图片 → 转存 S3 → 创建资产 → 更新状态
//   - 卡死任务恢复（服务重启后扫描 processing 超时任务）
//   - 资产访问 URL 生成
//
// 安全约束：
//   - 所有用户资源查询附带 user_id 条件
//   - 不向上层返回 Agnes API Key、AWS Secret、provider_credential_id（非管理员）
//   - 上游错误经过脱敏
//   - Presigned URL 短期有效，数据库只保存 S3 Object Key
type ImageGenerationService struct {
	conversationRepo ImageConversationRepository
	generationRepo   ImageGenerationRepository
	assetRepo        ImageAssetRepository
	credentialRepo   ImageCredentialRepository
	scheduler        CredentialScheduler
	storage          ImageObjectStorage
	encryptor        SecretEncryptor
	usageRepo        UsageLogRepository
	userRepo         UserRepository
	settingService   *SettingService
	cfg              config.ImageGenerationConfig
}

// NewImageGenerationService 构造图片生成服务。
func NewImageGenerationService(
	conversationRepo ImageConversationRepository,
	generationRepo ImageGenerationRepository,
	assetRepo ImageAssetRepository,
	credentialRepo ImageCredentialRepository,
	scheduler CredentialScheduler,
	storage ImageObjectStorage,
	encryptor SecretEncryptor,
	usageRepo UsageLogRepository,
	userRepo UserRepository,
	settingService *SettingService,
	cfg config.ImageGenerationConfig,
) *ImageGenerationService {
	return &ImageGenerationService{
		conversationRepo: conversationRepo,
		generationRepo:   generationRepo,
		assetRepo:        assetRepo,
		credentialRepo:   credentialRepo,
		scheduler:        scheduler,
		storage:          storage,
		encryptor:        encryptor,
		usageRepo:        usageRepo,
		userRepo:         userRepo,
		settingService:   settingService,
		cfg:              cfg,
	}
}

// ==================== 会话管理 ====================

// CreateConversation 创建用户的图片生成会话。
func (s *ImageGenerationService) CreateConversation(ctx context.Context, userID int64, title string) (*ImageConversation, error) {
	if !s.isEnabled() {
		return nil, errImageDisabled()
	}
	return s.conversationRepo.Create(ctx, CreateImageConversationParams{
		UserID: userID,
		Title:  title,
	})
}

// ListConversations 列出用户的会话（分页 + 关键词搜索）。
func (s *ImageGenerationService) ListConversations(ctx context.Context, userID int64, filter ImageConversationFilter) (*ImageConversationList, error) {
	if !s.isEnabled() {
		return nil, errImageDisabled()
	}
	filter.UserID = userID
	return s.conversationRepo.List(ctx, filter)
}

// GetConversation 获取用户会话详情（附带 user_id 隔离）。
func (s *ImageGenerationService) GetConversation(ctx context.Context, userID, id int64) (*ImageConversation, error) {
	if !s.isEnabled() {
		return nil, errImageDisabled()
	}
	return s.conversationRepo.GetByIDForOwner(ctx, userID, id)
}

// UpdateConversation 更新会话标题。
func (s *ImageGenerationService) UpdateConversation(ctx context.Context, userID, id int64, title string) (*ImageConversation, error) {
	if !s.isEnabled() {
		return nil, errImageDisabled()
	}
	trimmed := strings.TrimSpace(title)
	return s.conversationRepo.Update(ctx, userID, id, UpdateImageConversationParams{Title: &trimmed})
}

// DeleteConversation 软删除会话及其下所有生成任务和资产。
// S3 对象通过异步清理（本次实现先软删除数据库记录，S3 对象保留待后台补偿）。
func (s *ImageGenerationService) DeleteConversation(ctx context.Context, userID, id int64) error {
	if !s.isEnabled() {
		return errImageDisabled()
	}
	// 1. 软删除会话下所有生成任务的资产
	generations, err := s.generationRepo.ListByConversation(ctx, userID, id)
	if err != nil {
		return err
	}
	for _, gen := range generations {
		_ = s.assetRepo.SoftDeleteByGeneration(ctx, userID, gen.ID)
	}
	// 2. 软删除会话
	return s.conversationRepo.SoftDelete(ctx, userID, id)
}

// ==================== 生成任务 ====================

// CreateGenerationRequest 创建生成任务的请求参数。
type CreateGenerationRequest struct {
	ConversationID     *int64
	ParentGenerationID *int64
	Type               string // text_to_image | image_to_image
	Prompt             string
	Size               string
	Ratio              string
	InputAssetIDs      []int64 // 图生图的输入图片资产 ID
	IdempotencyKey     string
}

// CreateGeneration 创建生成任务。
//
// 流程：
//  1. 校验请求参数（prompt/size/ratio）
//  2. 幂等校验：若 idempotency_key 已存在，返回已有任务
//  3. 校验/创建会话
//  4. 图生图：校验输入资产归属
//  5. 创建 pending 状态的 generation 记录
//  6. 异步启动处理（goroutine）
//  7. 立即返回 generation_id
func (s *ImageGenerationService) CreateGeneration(ctx context.Context, userID int64, req CreateGenerationRequest) (*ImageGeneration, error) {
	if !s.isEnabled() {
		return nil, errImageDisabled()
	}

	// 1. 参数校验
	agnesReq := AgnesGenerateRequest{
		Model:  s.cfg.AgnesModel,
		Prompt: req.Prompt,
		Size:   req.Size,
		Ratio:  req.Ratio,
	}
	if err := ValidateAgnesRequest(agnesReq, s.cfg.MaxPromptChars); err != nil {
		return nil, err
	}
	if req.Type != ImageGenerationTypeTextToImage && req.Type != ImageGenerationTypeImageToImage {
		return nil, errImageInvalidRequest("invalid generation_type: " + req.Type)
	}
	if req.Type == ImageGenerationTypeImageToImage && len(req.InputAssetIDs) == 0 {
		return nil, errImageInputRequired()
	}
	if len(req.InputAssetIDs) > s.cfg.MaxInputImagesPerGen {
		return nil, errImageInvalidRequest(fmt.Sprintf("too many input images, max %d", s.cfg.MaxInputImagesPerGen))
	}

	// 1.5 余额预检：余额不足单张预估价则拒绝，避免 0 余额用户白嫖。
	// 预检是尽力而为的防护，并发竞态仍可能放行（由 DeductBalance 的透支兜底处理）。
	if s.userRepo != nil {
		tier := NormalizeImageBillingTierOrDefault(req.Size)
		unitPrice := s.resolveImageUnitPrice(ctx, tier)
		if unitPrice > 0 {
			user, err := s.userRepo.GetByID(ctx, userID)
			if err != nil {
				slog.Warn("image generation: failed to load user for balance precheck",
					"user_id", userID, "error", err)
			} else if user.Balance < unitPrice {
				slog.Info("image generation: rejected due to insufficient balance",
					"user_id", userID, "balance", user.Balance, "unit_price", unitPrice, "tier", tier)
				return nil, errImageInsufficientBalance()
			}
		}
	}

	// 2. 幂等校验
	if req.IdempotencyKey != "" {
		existing, err := s.generationRepo.GetByIdempotencyKey(ctx, userID, req.IdempotencyKey)
		if err != nil && !errors.Is(err, ErrImageGenerationNotFound) {
			return nil, err
		}
		if existing != nil {
			// 幂等键命中：返回已有任务（不重复创建、不重复扣费）
			return existing, nil
		}
	}

	// 3. 会话校验/创建
	var conversationID int64
	// 根据 prompt 计算"标题前缀"（前 30 字，超出加省略号）
	titleFromPrompt := req.Prompt
	if len([]rune(titleFromPrompt)) > 30 {
		titleFromPrompt = string([]rune(titleFromPrompt)[:30]) + "..."
	}
	if req.ConversationID != nil && *req.ConversationID > 0 {
		conv, err := s.conversationRepo.GetByIDForOwner(ctx, userID, *req.ConversationID)
		if err != nil {
			return nil, err
		}
		conversationID = conv.ID
		// 若会话标题仍是默认值（"新会话"或空），用 prompt 前缀更新
		if strings.TrimSpace(conv.Title) == "" || conv.Title == "新会话" {
			_, _ = s.conversationRepo.Update(ctx, userID, conversationID, UpdateImageConversationParams{Title: &titleFromPrompt})
		}
	} else {
		// 自动创建会话（用 prompt 前 30 字作为标题）
		conv, err := s.conversationRepo.Create(ctx, CreateImageConversationParams{
			UserID: userID,
			Title:  titleFromPrompt,
		})
		if err != nil {
			return nil, err
		}
		conversationID = conv.ID
	}

	// 4. 图生图：校验输入资产归属
	var inputAssets []*ImageAsset
	if req.Type == ImageGenerationTypeImageToImage {
		for _, assetID := range req.InputAssetIDs {
			asset, err := s.assetRepo.GetByIDForOwner(ctx, userID, assetID)
			if err != nil {
				return nil, err
			}
			if asset.AssetType != ImageAssetTypeInput {
				return nil, errImageInvalidRequest("input asset must be of type 'input'")
			}
			inputAssets = append(inputAssets, asset)
		}
	}

	// 5. 创建 generation 记录（带用户级并发检查）
	//    直接创建为 queued 状态：进入调度队列等待 Token 空闲。
	//    并发检查统计 pending+queued+processing，由 Repository 在事务内 advisory lock 保护避免竞态。
	var idemKey *string
	if req.IdempotencyKey != "" {
		idemKey = &req.IdempotencyKey
	}
	maxConcurrent := s.resolveMaxConcurrentPerUser(ctx)
	gen, err := s.generationRepo.CreateIfUnderUserConcurrency(ctx, CreateImageGenerationParams{
		UserID:             userID,
		ConversationID:     conversationID,
		ParentGenerationID: req.ParentGenerationID,
		Provider:           PlatformAgnes,
		Model:              s.cfg.AgnesModel,
		GenerationType:     req.Type,
		Prompt:             req.Prompt,
		Size:               req.Size,
		Ratio:              req.Ratio,
		Status:             ImageStatusQueued,
		IdempotencyKey:     idemKey,
	}, maxConcurrent)
	if err != nil {
		return nil, err
	}

	// 5.1 图生图：将 input 资产关联到当前 generation（ConfirmUpload 时 generation_id=0）
	if req.Type == ImageGenerationTypeImageToImage && len(inputAssets) > 0 {
		assetIDs := make([]int64, 0, len(inputAssets))
		for _, a := range inputAssets {
			assetIDs = append(assetIDs, a.ID)
		}
		if err := s.assetRepo.LinkAssetsToGeneration(ctx, userID, gen.ID, assetIDs); err != nil {
			return nil, err
		}
	}

	// 6. 异步调度（goroutine，使用独立 context 不受请求生命周期影响）
	//    tryDispatch 会尝试 ClaimQueued + TryAcquireCredential，
	//    若所有 Token 都被占用则保持 queued 状态等待 dispatcher ticker 调度。
	go s.tryDispatch(gen.ID, userID, conversationID, inputAssets, req)

	return gen, nil
}

// resolveMaxConcurrentPerUser 解析当前生效的用户级并发上限。
// 优先级：管理员后台 settings > config.yaml 默认值。
// settings 读取失败或未配置时回退到 cfg.MaxConcurrentPerUser；
// cfg 也 <= 0 时返回 0（表示不启用并发检查，退化为普通 Create）。
func (s *ImageGenerationService) resolveMaxConcurrentPerUser(ctx context.Context) int {
	if s.settingService != nil {
		if v, ok := s.settingService.GetImageMaxConcurrentPerUser(ctx); ok {
			return v
		}
	}
	return s.cfg.MaxConcurrentPerUser
}

// GetGeneration 获取生成任务详情（附带 user_id 隔离）。
func (s *ImageGenerationService) GetGeneration(ctx context.Context, userID, id int64) (*ImageGeneration, error) {
	if !s.isEnabled() {
		return nil, errImageDisabled()
	}
	return s.generationRepo.GetByIDForOwner(ctx, userID, id)
}

// ListGenerationsByConversation 列出会话下的所有生成任务。
func (s *ImageGenerationService) ListGenerationsByConversation(ctx context.Context, userID, conversationID int64) ([]*ImageGeneration, error) {
	if !s.isEnabled() {
		return nil, errImageDisabled()
	}
	// 先校验会话归属
	if _, err := s.conversationRepo.GetByIDForOwner(ctx, userID, conversationID); err != nil {
		return nil, err
	}
	return s.generationRepo.ListByConversation(ctx, userID, conversationID)
}

// ListGenerations 列出用户的生成任务（分页）。
func (s *ImageGenerationService) ListGenerations(ctx context.Context, userID int64, filter ImageGenerationFilter) (*ImageGenerationList, error) {
	if !s.isEnabled() {
		return nil, errImageDisabled()
	}
	filter.UserID = userID
	return s.generationRepo.List(ctx, filter)
}

// DeleteGeneration 软删除生成任务及其资产。
func (s *ImageGenerationService) DeleteGeneration(ctx context.Context, userID, id int64) error {
	if !s.isEnabled() {
		return errImageDisabled()
	}
	_ = s.assetRepo.SoftDeleteByGeneration(ctx, userID, id)
	return s.generationRepo.SoftDelete(ctx, userID, id)
}

// ==================== 异步处理（排队调度）====================

// tryDispatch 尝试调度一个 queued 任务：
//  1. ClaimQueued（CAS queued → processing，原子操作防止多节点重复调度）
//  2. 构造 Agnes 请求 + 图生图输入编码
//  3. 重试循环：TryAcquireCredential + GenerateWithCredential（最多 MaxAttempts 次）
//     - 所有 Token 忙碌：RevertToQueued 回退队列，等待 dispatcher ticker 调度
//     - 不可重试错误（参数/内容策略）：直接 markFailed
//     - 可重试错误（401/403/429/5xx/网络）：释放当前 Token，尝试下一个 Token
//  4. 成功：执行 executeGenerationAfterUpstream（下载/存储/创建资产/标记成功）
//
// 并发安全：ClaimQueued 使用 CAS，多节点不会重复调度同一任务。
// 调用方：CreateGeneration goroutine + Dispatcher ticker（通过 DispatchQueuedBatch 间接调用）。
func (s *ImageGenerationService) tryDispatch(generationID, userID, conversationID int64, inputAssets []*ImageAsset, req CreateGenerationRequest) {
	// 独立 context，超时为生成超时 + 下载超时 + S3 上传超时 + 缓冲
	totalTimeout := time.Duration(s.cfg.GenerateTimeoutSeconds+s.cfg.DownloadTimeoutSeconds+s.cfg.S3UploadTimeoutSeconds+30) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), totalTimeout)
	defer cancel()

	startedAt := time.Now()

	// 1. ClaimQueued（CAS queued → processing）
	claimed, err := s.generationRepo.ClaimQueued(ctx, generationID)
	if err != nil {
		slog.Error("image generation: ClaimQueued failed",
			"generation_id", generationID, "user_id", userID, "error", err)
		s.markFailed(ctx, userID, generationID, "STATE_UPDATE_FAILED", "failed to claim queued task", startedAt)
		return
	}
	if !claimed {
		// 任务已被其他流程认领或状态已变更（可能已被 dispatcher 调度或用户取消）
		slog.Debug("image generation: task already claimed or status changed",
			"generation_id", generationID, "user_id", userID)
		return
	}

	// 2. 构造 Agnes 请求
	agnesReq := AgnesGenerateRequest{
		Model:  s.cfg.AgnesModel,
		Prompt: req.Prompt,
		Size:   req.Size,
		Ratio:  req.Ratio,
	}

	// 3. 图生图：读取输入图片并 base64 编码（Agnes 期望 base64 数据，不是 URL）
	if req.Type == ImageGenerationTypeImageToImage && len(inputAssets) > 0 {
		b64s, err := s.encodeInputAssetsToBase64(ctx, userID, inputAssets)
		if err != nil {
			slog.Error("image generation: failed to encode input assets to base64",
				"generation_id", generationID, "user_id", userID, "error", err)
			s.markFailed(ctx, userID, generationID, "INPUT_READ_FAILED", sanitizeUpstreamErrorMessage(err.Error()), startedAt)
			return
		}
		agnesReq.InputImageBase64 = b64s
	}

	// 4. 重试循环：TryAcquireCredential + GenerateWithCredential
	//    Token 禁用/失效/调用失败时释放占用并尝试其他可用 Token（最多 MaxAttemptsPerGeneration 次）
	maxAttempts := 3
	if s.cfg.MaxAttemptsPerGeneration > 0 {
		maxAttempts = s.cfg.MaxAttemptsPerGeneration
	}

	var (
		credentialID int64
		result       *AgnesGenerateResult
		callErr      error
		release      func()
	)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// 4.1 占用一个空闲 Token
		credID, rel, ok := s.scheduler.TryAcquireCredential(ctx)
		if !ok {
			// 所有 Token 都被占用：回退到 queued，等待 dispatcher ticker 调度
			if rerr := s.generationRepo.RevertToQueued(ctx, generationID); rerr != nil {
				slog.Error("image generation: RevertToQueued failed",
					"generation_id", generationID, "user_id", userID, "error", rerr)
				// 回退失败：标记为 failed 避免任务卡在 processing
				s.markFailed(ctx, userID, generationID, "NO_AVAILABLE_CREDENTIAL", "all credentials busy and revert failed", startedAt)
			} else {
				slog.Info("image generation: all credentials busy, task reverted to queued",
					"generation_id", generationID, "user_id", userID, "attempt", attempt)
			}
			return
		}

		// 4.2 调用上游
		res, gerr := s.scheduler.GenerateWithCredential(ctx, credID, agnesReq)
		if gerr == nil {
			// 成功
			credentialID = credID
			result = res
			release = rel
			break
		}

		// 失败：释放当前 Token，记录错误
		rel()
		credentialID = credID
		callErr = gerr
		slog.Info("image generation: GenerateWithCredential failed, will retry with another credential",
			"generation_id", generationID, "user_id", userID,
			"credential_id", credID, "attempt", attempt, "error", gerr)

		// 不可重试错误（参数/内容策略）：直接 markFailed，不换 Token
		if appErr, ok := gerr.(*ImageGenError); ok {
			if appErr.Reason == "IMAGE_INVALID_REQUEST" {
				errCode := appErr.Reason
				errMsg := appErr.Message
				s.markFailedWithCredential(ctx, userID, generationID, credentialID, errCode, errMsg, startedAt)
				return
			}
		}
		// 可重试：继续下一个 Token
	}

	if callErr != nil {
		// 所有重试都失败
		errCode := "UPSTREAM_FAILED"
		errMsg := sanitizeUpstreamErrorMessage(callErr.Error())
		if appErr, ok := callErr.(*ImageGenError); ok {
			errCode = appErr.Reason
			errMsg = appErr.Message
		}
		s.markFailedWithCredential(ctx, userID, generationID, credentialID, errCode, errMsg, startedAt)
		return
	}
	defer release()

	// 5. 下载/存储/创建资产/标记成功
	s.executeGenerationAfterUpstream(ctx, userID, generationID, conversationID, req, credentialID, result, startedAt)
}

// executeGenerationAfterUpstream 执行上游调用成功后的步骤：
// 下载/解码上游图片 → 转存 S3 → 创建输出资产 → 更新状态 succeeded → 记录 usage。
// 入口前置条件：任务已 ClaimQueued（status=processing），上游调用已成功。
func (s *ImageGenerationService) executeGenerationAfterUpstream(
	ctx context.Context,
	userID, generationID, conversationID int64,
	req CreateGenerationRequest,
	credentialID int64,
	result *AgnesGenerateResult,
	startedAt time.Time,
) {
	// 1. 下载/解码上游图片并转存 S3
	outputKey := s.buildOutputS3Key(userID, conversationID, generationID, result.MimeType)
	downloadTimeout := time.Duration(s.cfg.DownloadTimeoutSeconds) * time.Second
	dialTimeout := time.Duration(s.cfg.GenerateDialTimeoutSeconds) * time.Second
	headerTimeout := time.Duration(s.cfg.GenerateResponseHeaderSeconds) * time.Second
	s3UploadTimeout := time.Duration(s.cfg.S3UploadTimeoutSeconds) * time.Second

	var stored *StoredObject
	if result.URL != "" {
		// URL 模式：流式下载到 S3（SSRF 防护在 DownloadImageToStorage 内）
		dlCtx, dlCancel := context.WithTimeout(ctx, downloadTimeout+s3UploadTimeout)
		defer dlCancel()
		stored = s.downloadAndStore(dlCtx, result.URL, outputKey, dialTimeout, headerTimeout, downloadTimeout, userID, generationID)
	} else if result.B64JSON != "" {
		// Base64 模式：解码后上传 S3
		stored = s.decodeAndStore(ctx, result.B64JSON, outputKey, result.MimeType, userID, generationID)
	}

	if stored == nil {
		s.markFailedWithCredential(ctx, userID, generationID, credentialID, "STORAGE_FAILED", "failed to store generated image", startedAt)
		return
	}

	// 2. 创建输出资产记录
	_, err := s.assetRepo.Create(ctx, CreateImageAssetParams{
		UserID:       userID,
		GenerationID: generationID,
		AssetType:    ImageAssetTypeOutput,
		S3Bucket:     stored.Bucket,
		S3Key:        stored.Key,
		MimeType:     stored.MimeType,
		FileSize:     stored.Size,
	})
	if err != nil {
		slog.Error("image generation: failed to create output asset",
			"generation_id", generationID, "user_id", userID, "error", err)
		s.markFailedWithCredential(ctx, userID, generationID, credentialID, "ASSET_CREATE_FAILED", "failed to create output asset record", startedAt)
		return
	}

	// 3. 更新任务状态为 succeeded
	completedAt := time.Now()
	durationMs := int(completedAt.Sub(startedAt).Milliseconds())
	_, err = s.generationRepo.UpdateStatus(ctx, userID, generationID, UpdateImageGenerationStatusParams{
		Status:               imgStrPtr(ImageStatusSucceeded),
		ProviderCredentialID: imgInt64Ptr(credentialID),
		ProviderOriginalURL:  imgStrPtr(result.URL), // 仅用于短期排障
		DurationMs:           &durationMs,
		CompletedAt:          &completedAt,
	})
	if err != nil {
		slog.Error("image generation: failed to mark succeeded",
			"generation_id", generationID, "user_id", userID, "error", err)
		return
	}

	// 4. 更新会话最近活动时间
	_ = s.conversationRepo.TouchLastMessageAt(ctx, userID, conversationID, completedAt)

	// 5. 记录 UsageLog（计量，不阻塞成功）
	s.recordUsage(ctx, userID, generationID, credentialID, req, durationMs, 1, true)

	slog.Info("image generation: succeeded",
		"generation_id", generationID, "user_id", userID,
		"credential_id", credentialID, "duration_ms", durationMs)
}

// DispatchQueuedBatch 扫描一批 queued 任务并尝试调度。
// 供 Dispatcher ticker 调用：对每个 queued 任务异步执行 tryDispatch。
// 返回本次成功认领（ClaimQueued 成功）的任务数。
//
// 注意：本方法不阻塞等待任务完成，仅负责触发调度。
// 每个任务在独立 goroutine 中执行，互不影响。
func (s *ImageGenerationService) DispatchQueuedBatch(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 || batchSize > 100 {
		batchSize = 20
	}

	tasks, err := s.generationRepo.ListQueued(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("list queued generations: %w", err)
	}
	if len(tasks) == 0 {
		return 0, nil
	}

	dispatched := 0
	for _, gen := range tasks {
		// 先检查是否有可用凭据，避免无意义的 ClaimQueued
		if !s.scheduler.HasAvailableCredential(ctx) {
			break
		}

		// 重新加载任务的输入资产（图生图场景）
		var inputAssets []*ImageAsset
		if gen.GenerationType == ImageGenerationTypeImageToImage {
			assets, err := s.assetRepo.ListByGeneration(ctx, gen.UserID, gen.ID)
			if err != nil {
				slog.Warn("image generation: failed to load input assets for queued task",
					"generation_id", gen.ID, "user_id", gen.UserID, "error", err)
				continue
			}
			for _, a := range assets {
				if a.AssetType == ImageAssetTypeInput {
					inputAssets = append(inputAssets, a)
				}
			}
		}

		// 重构 CreateGenerationRequest（仅保留调度所需字段）
		req := CreateGenerationRequest{
			Type:               gen.GenerationType,
			Prompt:             gen.Prompt,
			Size:               gen.Size,
			Ratio:              gen.Ratio,
			ConversationID:     &gen.ConversationID,
			ParentGenerationID: gen.ParentGenerationID,
		}

		go s.tryDispatch(gen.ID, gen.UserID, gen.ConversationID, inputAssets, req)
		dispatched++
	}
	return dispatched, nil
}

// encodeInputAssetsToBase64 读取输入图片存储对象并编码为 base64 字符串。
// Agnes 图生图 API 期望 extra_body.image 传入 base64 编码的图片数据（不带 data: 前缀），
// 而非 URL。调用方负责控制读取大小（MaxInputImageBytes）。
func (s *ImageGenerationService) encodeInputAssetsToBase64(ctx context.Context, userID int64, assets []*ImageAsset) ([]string, error) {
	if !s.storage.Configured() {
		return nil, errors.New("image storage is not configured")
	}
	maxBytes := s.cfg.MaxOutputImageBytes // 复用输出图大小上限约束单张输入图
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024 // 默认 10MB
	}
	result := make([]string, 0, len(assets))
	for _, a := range assets {
		rc, err := s.storage.Get(ctx, a.S3Key)
		if err != nil {
			return nil, fmt.Errorf("read input asset %d: %w", a.ID, err)
		}
		// 限制读取大小，防止超大文件导致内存溢出
		data, err := io.ReadAll(io.LimitReader(rc, int64(maxBytes)+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read input asset %d body: %w", a.ID, err)
		}
		if int64(len(data)) > int64(maxBytes) {
			return nil, fmt.Errorf("input asset %d exceeds %d bytes", a.ID, maxBytes)
		}
		result = append(result, base64.StdEncoding.EncodeToString(data))
	}
	return result, nil
}

// downloadAndStore 下载上游图片 URL 并转存 S3。
// 失败时返回 nil（调用方负责标记任务失败）。
func (s *ImageGenerationService) downloadAndStore(ctx context.Context, upstreamURL, destKey string, dialTimeout, headerTimeout, totalTimeout time.Duration, userID, generationID int64) *StoredObject {
	// 使用 repository.DownloadImageToStorage（含 SSRF 防护）
	// 该函数在 repository 包中，service 层通过 storage 接口间接调用。
	// 这里直接调用 storage.Put，下载部分由 service 层用安全 HTTP 客户端完成。
	maxBytes := s.cfg.MaxOutputImageBytes
	if maxBytes <= 0 {
		maxBytes = 20 * 1024 * 1024 // 默认 20MB
	}

	// 复用 repository 包的 DownloadImageToStorage 函数（已实现 SSRF 防护 + 流式上传）
	// 但由于该函数在 repository 包，这里通过 storage 接口实现等价逻辑。
	// 为避免循环依赖，这里直接使用 storage.Put 并在 service 层实现下载。
	stored, err := downloadAndStoreImage(ctx, upstreamURL, destKey, s.storage, maxBytes, dialTimeout, headerTimeout, totalTimeout)
	if err != nil {
		slog.Error("image generation: download/store failed",
			"generation_id", generationID, "user_id", userID, "error", err)
		return nil
	}
	return stored
}

// decodeAndStore 解码 Base64 图片并上传 S3。
func (s *ImageGenerationService) decodeAndStore(ctx context.Context, b64, destKey, mimeType string, userID, generationID int64) *StoredObject {
	stored, err := decodeAndStoreBase64Image(ctx, b64, destKey, mimeType, s.storage, s.cfg.MaxOutputImageBytes)
	if err != nil {
		slog.Error("image generation: decode/store base64 failed",
			"generation_id", generationID, "user_id", userID, "error", err)
		return nil
	}
	return stored
}

// markFailed 标记任务失败（无 credentialID）。
func (s *ImageGenerationService) markFailed(ctx context.Context, userID, generationID int64, errCode, errMsg string, startedAt time.Time) {
	s.markFailedWithCredential(ctx, userID, generationID, 0, errCode, errMsg, startedAt)
}

// markFailedWithCredential 标记任务失败，记录 credentialID（可能为 0）。
func (s *ImageGenerationService) markFailedWithCredential(ctx context.Context, userID, generationID, credentialID int64, errCode, errMsg string, startedAt time.Time) {
	completedAt := time.Now()
	durationMs := int(completedAt.Sub(startedAt).Milliseconds())
	params := UpdateImageGenerationStatusParams{
		Status:       imgStrPtr(ImageStatusFailed),
		ErrorCode:    imgStrPtr(errCode),
		ErrorMessage: imgStrPtr(errMsg),
		DurationMs:   &durationMs,
		CompletedAt:  &completedAt,
	}
	if credentialID > 0 {
		params.ProviderCredentialID = imgInt64Ptr(credentialID)
	}
	_, err := s.generationRepo.UpdateStatus(ctx, userID, generationID, params)
	if err != nil {
		slog.Error("image generation: failed to mark task as failed",
			"generation_id", generationID, "user_id", userID, "error", err)
	}
}

// recordUsage 记录用量到 UsageLog 并扣费（计量，不阻塞主流程）。
//
// 计费规则：
//   - 仅在生成成功（succeeded=true）时扣费；失败不扣。
//   - 按 req.Size 解析到 1K/2K/3K/4K tier，按 tier 取单价：
//     优先使用管理员后台 settings 配置（SettingKeyImagePriceConfig），
//     未配置的 tier 回退到 config.yaml 的 image_generation.price_*_usd 默认值。
//   - cost = 单价 × imageCount。
//   - 通过 UserRepository.DeductBalance 扣除用户余额，允许透支（余额可变为负数），
//     与项目内支付退款路径一致；扣费失败只记录 warn 日志，不影响生成成功响应。
//   - UsageLog 同时写入 total_cost/actual_cost/billing_mode="image"/image_size=tier，
//     前端使用记录页（UsageTable）据此展示。
func (s *ImageGenerationService) recordUsage(ctx context.Context, userID, generationID, credentialID int64, req CreateGenerationRequest, durationMs int, imageCount int, succeeded bool) {
	if s.usageRepo == nil {
		return
	}
	fingerprint := ""
	if credentialID > 0 {
		cred, err := s.credentialRepo.GetByID(ctx, credentialID)
		if err == nil {
			fingerprint = cred.KeyFingerprint
		}
	}
	_ = fingerprint // 仅用于日志，不写入 UsageLog（避免泄露）

	// 解析 size 到 tier（未识别时归一化为 2K）
	tier := NormalizeImageBillingTierOrDefault(req.Size)

	// 计算扣费金额：仅成功时计费
	var cost float64
	if succeeded && imageCount > 0 {
		unitPrice := s.resolveImageUnitPrice(ctx, tier)
		cost = unitPrice * float64(imageCount)
		if cost < 0 {
			cost = 0
		}
	}

	// 扣费：容错，失败不阻塞主流程
	if cost > 0 && s.userRepo != nil {
		if err := s.userRepo.DeductBalance(ctx, userID, cost); err != nil {
			slog.Warn("image generation: failed to deduct balance",
				"generation_id", generationID, "user_id", userID, "cost", cost, "tier", tier, "error", err)
		}
	}

	billingMode := "image"
	log := &UsageLog{
		UserID:      userID,
		Model:       ImageGenerationDisplayModel,
		RequestID:   fmt.Sprintf("imggen-%d", generationID),
		ImageCount:  imageCount,
		ImageSize:   &tier,
		BillingMode: &billingMode,
		DurationMs:  &durationMs,
		TotalCost:   cost,
		ActualCost:  cost,
	}
	inserted, err := s.usageRepo.Create(ctx, log)
	if err != nil {
		slog.Warn("image generation: failed to record usage log",
			"generation_id", generationID, "user_id", userID, "error", err)
	} else if !inserted {
		slog.Debug("image generation: usage log skipped (duplicate)",
			"generation_id", generationID)
	}
}

// resolveImageUnitPrice 解析指定 tier 的单张图片扣费单价（美元）。
// 优先级：管理员后台 settings 配置 > config.yaml 默认值。
// settings 读取失败或未配置该 tier 时回退到 config 默认值，保证容错。
func (s *ImageGenerationService) resolveImageUnitPrice(ctx context.Context, tier string) float64 {
	// 默认值兜底
	defaultPrice := s.defaultPriceForTier(tier)

	if s.settingService == nil {
		return defaultPrice
	}
	cfg, err := s.settingService.GetImagePriceConfig(ctx)
	if err != nil {
		slog.Warn("image generation: failed to load image price config from settings, using default",
			"tier", tier, "error", err)
		return defaultPrice
	}
	if cfg == nil {
		return defaultPrice
	}

	switch tier {
	case ImageBillingSize1K:
		if cfg.Price1K != nil {
			return *cfg.Price1K
		}
	case ImageBillingSize2K:
		if cfg.Price2K != nil {
			return *cfg.Price2K
		}
	case ImageBillingSize3K:
		if cfg.Price3K != nil {
			return *cfg.Price3K
		}
	case ImageBillingSize4K:
		if cfg.Price4K != nil {
			return *cfg.Price4K
		}
	}
	return defaultPrice
}

// defaultPriceForTier 返回 config.yaml 中该 tier 的默认单价。
// 未识别的 tier 按 2K 处理（与 NormalizeImageBillingTierOrDefault 一致）。
func (s *ImageGenerationService) defaultPriceForTier(tier string) float64 {
	switch tier {
	case ImageBillingSize1K:
		return s.cfg.Price1KUSD
	case ImageBillingSize2K:
		return s.cfg.Price2KUSD
	case ImageBillingSize3K:
		return s.cfg.Price3KUSD
	case ImageBillingSize4K:
		return s.cfg.Price4KUSD
	default:
		return s.cfg.Price2KUSD
	}
}

// ==================== 卡死任务恢复 ====================

// RecoverStaleGenerations 扫描卡在 processing 且超时的任务，标记为 failed。
// 应在服务启动时和定时任务中调用。
func (s *ImageGenerationService) RecoverStaleGenerations(ctx context.Context) (int, error) {
	thresholdSeconds := s.cfg.StaleProcessingAfterSeconds
	if thresholdSeconds <= 0 {
		thresholdSeconds = 300 // 默认 5 分钟
	}
	cutoff := time.Now().Add(-time.Duration(thresholdSeconds) * time.Second)

	stale, err := s.generationRepo.ListStaleProcessing(ctx, StaleProcessingFilter{
		BeforeTime: cutoff,
		Limit:      100,
	})
	if err != nil {
		return 0, err
	}

	recovered := 0
	for _, gen := range stale {
		// 标记为 failed（使用内部 GetByID 不需要 user_id，但更新需要 user_id）
		// 这里用 gen.UserID 更新
		now := time.Now()
		_, err := s.generationRepo.UpdateStatus(ctx, gen.UserID, gen.ID, UpdateImageGenerationStatusParams{
			Status:       imgStrPtr(ImageStatusFailed),
			ErrorCode:    imgStrPtr("STALE_TIMEOUT"),
			ErrorMessage: imgStrPtr("task was stuck in processing state and has been recovered"),
			CompletedAt:  &now,
		})
		if err != nil {
			slog.Error("image generation: failed to recover stale task",
				"generation_id", gen.ID, "user_id", gen.UserID, "error", err)
			continue
		}
		recovered++
		slog.Info("image generation: recovered stale task",
			"generation_id", gen.ID, "user_id", gen.UserID)
	}
	return recovered, nil
}

// ==================== 辅助方法 ====================

func (s *ImageGenerationService) isEnabled() bool {
	return s.cfg.Enabled
}

// buildOutputS3Key 构造输出图片的 S3 Key。
// 格式：media/images/{user_id}/{yyyy}/{mm}/{conversation_id}/{generation_id}/output/{uuid}.{ext}
func (s *ImageGenerationService) buildOutputS3Key(userID, conversationID, generationID int64, mimeType string) string {
	now := time.Now()
	ext := mimeTypeToExt(mimeType)
	uuid := randomImageHex(8)
	return fmt.Sprintf("media/images/%d/%04d/%02d/%d/%d/output/%s.%s",
		userID, now.Year(), int(now.Month()), conversationID, generationID, uuid, ext)
}

// BuildInputS3Key 构造用户上传输入图片的 S3 Key。
// 格式：media/images/{user_id}/{yyyy}/{mm}/uploads/{uuid}.{ext}
func (s *ImageGenerationService) BuildInputS3Key(userID int64, mimeType string) string {
	now := time.Now()
	ext := mimeTypeToExt(mimeType)
	uuid := randomImageHex(8)
	return fmt.Sprintf("media/images/%d/%04d/%02d/uploads/%s.%s",
		userID, now.Year(), int(now.Month()), uuid, ext)
}

// ==================== 内部辅助函数 ====================

func imgStrPtr(s string) *string { return &s }
func imgInt64Ptr(v int64) *int64 { return &v }

func mimeTypeToExt(mime string) string {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
}

func randomImageHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
