package service

import (
	"context"
	"time"
)

// 图片生成相关枚举常量。与 ent 生成的 enum 保持一致，但使用 service 包的字符串类型，
// 避免 service 层直接依赖 ent 包。

// GenerationType
const (
	ImageGenerationTypeTextToImage  = "text_to_image"
	ImageGenerationTypeImageToImage = "image_to_image"
)

// Generation Status
const (
	ImageStatusPending   = "pending"
	ImageStatusQueued    = "queued"
	ImageStatusProcessing = "processing"
	ImageStatusSucceeded  = "succeeded"
	ImageStatusFailed    = "failed"
	ImageStatusCanceled  = "canceled"
)

// Asset Type
const (
	ImageAssetTypeInput    = "input"
	ImageAssetTypeOutput   = "output"
	ImageAssetTypeThumbnail = "thumbnail"
)

// Credential Status
const (
	ImageCredentialStatusHealthy   = "healthy"
	ImageCredentialStatusUnhealthy = "unhealthy"
	ImageCredentialStatusDisabled  = "disabled"
)

// ImageConversation 表示用户的图片生成会话。
type ImageConversation struct {
	ID            int64
	UserID        int64
	Title         string
	LastMessageAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time
}

// CreateImageConversationParams 创建会话参数。
type CreateImageConversationParams struct {
	UserID int64
	Title  string
}

// UpdateImageConversationParams 更新会话参数。
type UpdateImageConversationParams struct {
	Title *string
}

// ImageConversationFilter 会话列表过滤条件。
type ImageConversationFilter struct {
	UserID        int64
	Keyword       string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Page          int
	PageSize      int
}

// ImageConversationList 会话列表结果（含分页）。
type ImageConversationList struct {
	Items      []*ImageConversation
	Total      int64
	Page       int
	PageSize   int
}

// ImageGeneration 表示一次图片生成请求。
type ImageGeneration struct {
	ID                  int64
	UserID              int64
	ConversationID      int64
	ParentGenerationID  *int64
	Provider            string
	ProviderCredentialID *int64
	Model               string
	GenerationType      string
	Prompt              string
	Size                string
	Ratio               string
	Status              string
	IdempotencyKey      *string
	ProviderRequestID   *string
	ProviderOriginalURL *string
	ErrorCode           *string
	ErrorMessage        *string
	DurationMs          int
	StartedAt           *time.Time
	CompletedAt         *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// CreateImageGenerationParams 创建生成任务参数。
type CreateImageGenerationParams struct {
	UserID             int64
	ConversationID     int64
	ParentGenerationID *int64
	Provider           string
	Model              string
	GenerationType     string
	Prompt             string
	Size               string
	Ratio              string
	Status             string
	IdempotencyKey     *string
}

// UpdateImageGenerationStatusParams 更新生成任务状态参数。
// 所有字段为指针类型，nil 表示不更新该字段。
type UpdateImageGenerationStatusParams struct {
	Status              *string
	ProviderCredentialID *int64
	ProviderRequestID   *string
	ProviderOriginalURL *string
	ErrorCode           *string
	ErrorMessage        *string
	DurationMs          *int
	StartedAt           *time.Time
	CompletedAt         *time.Time
}

// ImageGenerationFilter 生成任务列表过滤条件。
type ImageGenerationFilter struct {
	UserID         int64
	ConversationID *int64
	Status         string
	Page           int
	PageSize       int
}

// ImageGenerationList 生成任务列表结果。
type ImageGenerationList struct {
	Items    []*ImageGeneration
	Total    int64
	Page     int
	PageSize int
}

// StaleProcessingFilter 用于扫描卡在 processing 的任务。
type StaleProcessingFilter struct {
	BeforeTime time.Time
	Limit      int
}

// ImageAsset 表示输入或输出图片的元数据。
type ImageAsset struct {
	ID               int64
	UserID           int64
	GenerationID     int64
	AssetType        string
	S3Bucket         string
	S3Key            string
	MimeType         string
	FileSize         int64
	Width            *int
	Height           *int
	SHA256           *string
	OriginalFilename *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// CreateImageAssetParams 创建图片资产参数。
type CreateImageAssetParams struct {
	UserID           int64
	GenerationID     int64
	AssetType        string
	S3Bucket         string
	S3Key            string
	MimeType         string
	FileSize         int64
	Width            *int
	Height           *int
	SHA256           *string
	OriginalFilename *string
}

// ImageAssetFilter 图片资产过滤条件。
type ImageAssetFilter struct {
	UserID       int64
	GenerationID *int64
	AssetType    string
}

// ImageConversationRepository 是会话表的 repository 接口。
// 所有查询必须附带 user_id 条件，避免越权访问。
type ImageConversationRepository interface {
	Create(ctx context.Context, params CreateImageConversationParams) (*ImageConversation, error)
	GetByIDForOwner(ctx context.Context, userID, id int64) (*ImageConversation, error)
	List(ctx context.Context, filter ImageConversationFilter) (*ImageConversationList, error)
	Update(ctx context.Context, userID, id int64, params UpdateImageConversationParams) (*ImageConversation, error)
	// TouchLastMessageAt 更新会话最近活动时间（best-effort）。
	TouchLastMessageAt(ctx context.Context, userID, id int64, at time.Time) error
	// SoftDelete 软删除会话（同步软删除其下所有 generation 和 asset 由 service 层负责）。
	SoftDelete(ctx context.Context, userID, id int64) error
}

// ImageGenerationRepository 是生成任务表的 repository 接口。
// 所有查询必须附带 user_id 条件，避免越权访问。
type ImageGenerationRepository interface {
	Create(ctx context.Context, params CreateImageGenerationParams) (*ImageGeneration, error)
	// CreateIfUnderUserConcurrency 在事务内原子地检查用户活跃任务数并创建新任务。
	// 使用 pg_advisory_xact_lock(user_id) 序列化同一用户的并发创建请求。
	// 当活跃任务数（pending+queued+processing）>= maxConcurrent 时返回 ErrImageConcurrentLimitReached。
	// maxConcurrent <= 0 时退化为普通 Create（不检查并发）。
	CreateIfUnderUserConcurrency(ctx context.Context, params CreateImageGenerationParams, maxConcurrent int) (*ImageGeneration, error)
	GetByIDForOwner(ctx context.Context, userID, id int64) (*ImageGeneration, error)
	GetByID(ctx context.Context, id int64) (*ImageGeneration, error)
	// GetByIdempotencyKey 按 (user_id, idempotency_key) 查询，用于幂等校验。
	GetByIdempotencyKey(ctx context.Context, userID int64, key string) (*ImageGeneration, error)
	List(ctx context.Context, filter ImageGenerationFilter) (*ImageGenerationList, error)
	// ListByConversation 列出会话下的所有生成任务（按 created_at 升序）。
	ListByConversation(ctx context.Context, userID, conversationID int64) ([]*ImageGeneration, error)
	// UpdateStatus 原子更新状态字段。
	UpdateStatus(ctx context.Context, userID, id int64, params UpdateImageGenerationStatusParams) (*ImageGeneration, error)
	// ListStaleProcessing 列出卡在 processing 且早于指定时间的任务（服务重启恢复用）。
	ListStaleProcessing(ctx context.Context, filter StaleProcessingFilter) ([]*ImageGeneration, error)
	// ListQueued 列出 queued 状态的任务（按 created_at 升序，FIFO），供 dispatcher 调度。
	// 不附带 user_id 过滤——这是内部系统调度操作，调用方不将结果返回给用户。
	ListQueued(ctx context.Context, limit int) ([]*ImageGeneration, error)
	// ClaimQueued 原子地将任务从 queued 更新为 processing（CAS），防止多节点重复调度。
	// 返回 true 表示成功认领；false 表示任务已被其他流程认领或状态已变更。
	ClaimQueued(ctx context.Context, taskID int64) (bool, error)
	// RevertToQueued 将 processing 回退为 queued（补偿操作：凭据获取失败时恢复）。
	// 清除 started_at 以避免被卡死任务恢复误判。
	RevertToQueued(ctx context.Context, taskID int64) error
	// CountActiveByUser 统计用户活跃任务数（pending+queued+processing），供管理员查看。
	CountActiveByUser(ctx context.Context, userID int64) (int, error)
	SoftDelete(ctx context.Context, userID, id int64) error
}

// ImageAssetRepository 是图片资产表的 repository 接口。
type ImageAssetRepository interface {
	Create(ctx context.Context, params CreateImageAssetParams) (*ImageAsset, error)
	GetByIDForOwner(ctx context.Context, userID, id int64) (*ImageAsset, error)
	// GetByS3Key 按 s3_key 查询（用于校验用户上传的输入图片归属）。
	GetByS3KeyForOwner(ctx context.Context, userID int64, s3Key string) (*ImageAsset, error)
	List(ctx context.Context, filter ImageAssetFilter) ([]*ImageAsset, error)
	// ListByGeneration 列出某次生成任务的所有资产（按 asset_type 升序）。
	ListByGeneration(ctx context.Context, userID, generationID int64) ([]*ImageAsset, error)
	// LinkAssetsToGeneration 将已存在的 input 资产关联到指定生成任务（更新 generation_id）。
	// 用于图生图场景：ConfirmUpload 时 generation_id=0，CreateGeneration 后才建立关联。
	LinkAssetsToGeneration(ctx context.Context, userID, generationID int64, assetIDs []int64) error
	SoftDelete(ctx context.Context, userID, id int64) error
	// SoftDeleteByGeneration 软删除某次生成任务的所有资产。
	SoftDeleteByGeneration(ctx context.Context, userID, generationID int64) error

	// ===== 资产清理（管理员/后台调度专用，跨用户）=====
	//
	// 以下方法用于清理"已软删除但文件仍存在"的孤立资产。
	// 调用方必须使用 mixins.SkipSoftDelete(ctx) 绕过软删除拦截器，
	// 否则已软删除的记录会被自动过滤，无法被列出/物理删除。
	//
	// ListSoftDeletedBefore 列出 deleted_at IS NOT NULL 且 deleted_at < cutoff 的资产。
	// 按 deleted_at 升序，限制 limit 条。用于定时扫描和管理员一键清理。
	ListSoftDeletedBefore(ctx context.Context, cutoff time.Time, limit int) ([]*ImageAsset, error)
	// CountSoftDeletedBefore 统计 deleted_at < cutoff 的已软删除资产数量（用于清理预览）。
	CountSoftDeletedBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// HardDelete 物理删除资产记录。仅在已确认存储对象被清理后调用。
	HardDelete(ctx context.Context, id int64) error
}
