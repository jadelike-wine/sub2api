package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ImageAssetService 处理图片资产的上传、确认和访问。
//
// 上传流程：
//  1. 前端申请 Presigned PUT URL → PresignUpload
//  2. 前端直接上传到 S3
//  3. 前端提交 S3 Object Key → ConfirmUpload
//  4. 后端校验 Object Key 属于当前用户 + Head 对象存在 + Content-Type/大小校验
//  5. 创建 ImageAsset 记录
type ImageAssetService struct {
	assetRepo ImageAssetRepository
	storage   ImageObjectStorage
	cfg       ImageStorageConfig
	maxBytes  int64
}

// NewImageAssetService 构造资产服务。
func NewImageAssetService(
	assetRepo ImageAssetRepository,
	storage ImageObjectStorage,
	maxInputBytes int64,
) *ImageAssetService {
	return &ImageAssetService{
		assetRepo: assetRepo,
		storage:   storage,
		maxBytes:  maxInputBytes,
	}
}

// PresignUploadRequest 预签名上传请求。
type PresignUploadRequest struct {
	MimeType string // image/png | image/jpeg | image/webp
}

// PresignUploadResponse 预签名上传响应。
type PresignUploadResponse struct {
	UploadURL  string // 短期 Presigned PUT URL
	S3Key      string // 前端上传后需回传此 key
	ExpiresIn  int    // URL 有效期（秒）
}

// PresignUpload 生成用户直传 S3 的预签名 PUT URL。
//
// 安全：
//   - S3 Key 必须位于当前用户目录（media/images/{user_id}/...）
//   - Content-Type 必须是允许的图片类型
//   - URL 短期有效（10-30 分钟）
func (s *ImageAssetService) PresignUpload(ctx context.Context, userID int64, req PresignUploadRequest) (*PresignUploadResponse, error) {
	if !s.storage.Configured() {
		return nil, errStorageNotConfigured
	}
	mime := strings.ToLower(strings.TrimSpace(req.MimeType))
	if !isAllowedImageMime(mime) {
		return nil, errImageInputUnsupported(mime)
	}

	// 构造用户专属 S3 Key
	s3Key := s.buildUserUploadKey(userID, mime)

	expires := 15 * time.Minute
	uploadURL, err := s.storage.PresignPut(ctx, s3Key, mime, expires)
	if err != nil {
		return nil, errImageStorageFailed()
	}

	return &PresignUploadResponse{
		UploadURL: uploadURL,
		S3Key:     s3Key,
		ExpiresIn: int(expires.Seconds()),
	}, nil
}

// ConfirmUploadRequest 确认上传请求。
type ConfirmUploadRequest struct {
	S3Key            string
	MimeType         string
	OriginalFilename *string
}

// ConfirmUpload 校验用户上传的 S3 对象并创建资产记录。
//
// 校验项：
//   - S3 Key 必须位于当前用户目录
//   - 对象必须存在（Head）
//   - Content-Type 必须是允许的图片类型
//   - 文件大小不超过限制
//
// 注意：此方法创建的资产 asset_type=input，generation_id=0（未关联到具体生成任务）。
// 创建生成任务时，通过 input_asset_ids 引用这些资产。
func (s *ImageAssetService) ConfirmUpload(ctx context.Context, userID int64, req ConfirmUploadRequest) (*ImageAsset, error) {
	if !s.storage.Configured() {
		return nil, errStorageNotConfigured
	}
	s3Key := strings.TrimSpace(req.S3Key)
	if s3Key == "" {
		return nil, errImageInvalidRequest("s3_key is required")
	}
	// 校验 S3 Key 属于当前用户目录
	if !s.isOwnedByUser(s3Key, userID) {
		return nil, errImageInputKeyNotOwned()
	}

	// Head 对象校验存在性和元数据
	head, err := s.storage.Head(ctx, s3Key)
	if err != nil {
		return nil, errImageStorageFailed()
	}
	if !head.Exists {
		return nil, errImageInvalidRequest("uploaded object not found")
	}

	// Content-Type 校验
	mime := strings.ToLower(strings.TrimSpace(head.ContentType))
	if mime == "" {
		mime = strings.ToLower(strings.TrimSpace(req.MimeType))
	}
	if !isAllowedImageMime(mime) {
		return nil, errImageInputUnsupported(mime)
	}

	// 文件大小校验
	maxBytes := s.maxBytes
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024 // 默认 10MB
	}
	if head.Size > maxBytes {
		return nil, errImageInputTooLarge()
	}

	// 创建资产记录（generation_id=0，未关联到具体生成任务）
	asset, err := s.assetRepo.Create(ctx, CreateImageAssetParams{
		UserID:           userID,
		GenerationID:     0, // 未关联；创建生成任务时关联
		AssetType:        ImageAssetTypeInput,
		S3Bucket:         head.Bucket,
		S3Key:            s3Key,
		MimeType:         mime,
		FileSize:         head.Size,
		OriginalFilename: req.OriginalFilename,
	})
	if err != nil {
		return nil, err
	}
	return asset, nil
}

// GetAssetAccessURL 生成资产的短期访问 URL（Presigned GET）。
// 用于前端查看图片。URL 短期有效，过期后需重新请求。
func (s *ImageAssetService) GetAssetAccessURL(ctx context.Context, userID, assetID int64, expires time.Duration) (string, error) {
	if !s.storage.Configured() {
		return "", errStorageNotConfigured
	}
	asset, err := s.assetRepo.GetByIDForOwner(ctx, userID, assetID)
	if err != nil {
		return "", err
	}
	if expires <= 0 {
		expires = 30 * time.Minute
	}
	return s.storage.PresignGet(ctx, asset.S3Key, expires)
}

// GetAssetsByGeneration 获取某次生成任务的所有资产（输入+输出）。
func (s *ImageAssetService) GetAssetsByGeneration(ctx context.Context, userID, generationID int64) ([]*ImageAsset, error) {
	return s.assetRepo.ListByGeneration(ctx, userID, generationID)
}

// GetAssetAccessURLsByGeneration 批量获取某次生成任务所有资产的访问 URL。
func (s *ImageAssetService) GetAssetAccessURLsByGeneration(ctx context.Context, userID, generationID int64, expires time.Duration) (map[int64]string, error) {
	assets, err := s.assetRepo.ListByGeneration(ctx, userID, generationID)
	if err != nil {
		return nil, err
	}
	if !s.storage.Configured() {
		return nil, errStorageNotConfigured
	}
	if expires <= 0 {
		expires = 30 * time.Minute
	}
	urls := make(map[int64]string, len(assets))
	for _, a := range assets {
		u, err := s.storage.PresignGet(ctx, a.S3Key, expires)
		if err != nil {
			return nil, err
		}
		urls[a.ID] = u
	}
	return urls, nil
}

// ==================== 辅助方法 ====================

// buildUserUploadKey 构造用户上传输入图片的 S3 Key。
// 格式：media/images/{user_id}/{yyyy}/{mm}/uploads/{uuid}.{ext}
func (s *ImageAssetService) buildUserUploadKey(userID int64, mimeType string) string {
	now := time.Now()
	ext := mimeTypeToExt(mimeType)
	uuid := randomImageHex(8)
	return fmt.Sprintf("media/images/%d/%04d/%02d/uploads/%s.%s",
		userID, now.Year(), int(now.Month()), uuid, ext)
}

// isOwnedByUser 校验 S3 Key 是否位于当前用户目录。
// 防止用户提交任意 S3 Key 后越权访问其他用户的对象。
func (s *ImageAssetService) isOwnedByUser(s3Key string, userID int64) bool {
	expectedPrefix := fmt.Sprintf("media/images/%d/", userID)
	return strings.HasPrefix(s3Key, expectedPrefix)
}
