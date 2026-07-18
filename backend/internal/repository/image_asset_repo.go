package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/imageasset"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type imageAssetRepository struct {
	client *ent.Client
}

// NewImageAssetRepository 构造图片资产仓库。
// 所有查询强制附带 user_id 条件，避免跨用户越权访问。
// SoftDeleteByGeneration 用于级联软删除某次生成任务的所有资产。
func NewImageAssetRepository(client *ent.Client) service.ImageAssetRepository {
	return &imageAssetRepository{client: client}
}

func (r *imageAssetRepository) Create(ctx context.Context, params service.CreateImageAssetParams) (*service.ImageAsset, error) {
	b := r.client.ImageAsset.Create().
		SetUserID(params.UserID).
		SetGenerationID(params.GenerationID).
		SetAssetType(imageasset.AssetType(params.AssetType)).
		SetS3Bucket(params.S3Bucket).
		SetS3Key(params.S3Key).
		SetMimeType(params.MimeType).
		SetFileSize(params.FileSize)
	if params.Width != nil {
		b.SetWidth(*params.Width)
	}
	if params.Height != nil {
		b.SetHeight(*params.Height)
	}
	if params.SHA256 != nil && *params.SHA256 != "" {
		b.SetSha256(*params.SHA256)
	}
	if params.OriginalFilename != nil && *params.OriginalFilename != "" {
		b.SetOriginalFilename(*params.OriginalFilename)
	}
	m, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return assetToService(m), nil
}

func (r *imageAssetRepository) GetByIDForOwner(ctx context.Context, userID, id int64) (*service.ImageAsset, error) {
	m, err := r.client.ImageAsset.Query().
		Where(
			imageasset.IDEQ(id),
			imageasset.UserIDEQ(userID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrImageAssetNotFound
		}
		return nil, err
	}
	return assetToService(m), nil
}

// GetByS3KeyForOwner 按 s3_key 查询，用于校验用户上传的输入图片归属。
// 这是防止越权访问的关键校验：用户提交 input_asset_ids 或 s3_key 时，
// 必须确认该资产属于当前用户目录。
func (r *imageAssetRepository) GetByS3KeyForOwner(ctx context.Context, userID int64, s3Key string) (*service.ImageAsset, error) {
	if s3Key == "" {
		return nil, service.ErrImageAssetNotFound
	}
	m, err := r.client.ImageAsset.Query().
		Where(
			imageasset.UserIDEQ(userID),
			imageasset.S3KeyEQ(s3Key),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrImageAssetNotFound
		}
		return nil, err
	}
	return assetToService(m), nil
}

func (r *imageAssetRepository) List(ctx context.Context, filter service.ImageAssetFilter) ([]*service.ImageAsset, error) {
	q := r.client.ImageAsset.Query().Where(imageasset.UserIDEQ(filter.UserID))
	if filter.GenerationID != nil {
		q = q.Where(imageasset.GenerationIDEQ(*filter.GenerationID))
	}
	if filter.AssetType != "" {
		q = q.Where(imageasset.AssetTypeEQ(imageasset.AssetType(filter.AssetType)))
	}
	items, err := q.
		Order(ent.Desc(imageasset.FieldCreatedAt), ent.Desc(imageasset.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.ImageAsset, 0, len(items))
	for _, m := range items {
		out = append(out, assetToService(m))
	}
	return out, nil
}

func (r *imageAssetRepository) ListByGeneration(ctx context.Context, userID, generationID int64) ([]*service.ImageAsset, error) {
	items, err := r.client.ImageAsset.Query().
		Where(
			imageasset.UserIDEQ(userID),
			imageasset.GenerationIDEQ(generationID),
		).
		Order(ent.Asc(imageasset.FieldAssetType), ent.Asc(imageasset.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.ImageAsset, 0, len(items))
	for _, m := range items {
		out = append(out, assetToService(m))
	}
	return out, nil
}

// LinkAssetsToGeneration 将已存在的 input 资产关联到指定生成任务。
// 仅更新 generation_id 字段，强制 user_id 隔离防止跨用户越权。
func (r *imageAssetRepository) LinkAssetsToGeneration(ctx context.Context, userID, generationID int64, assetIDs []int64) error {
	if len(assetIDs) == 0 {
		return nil
	}
	_, err := r.client.ImageAsset.Update().
		Where(
			imageasset.UserIDEQ(userID),
			imageasset.IDIn(assetIDs...),
		).
		SetGenerationID(generationID).
		Save(ctx)
	return err
}

func (r *imageAssetRepository) SoftDelete(ctx context.Context, userID, id int64) error {
	// SoftDeleteMixin 的 Hook 会把 Delete 转成 UPDATE deleted_at = NOW()
	affected, err := r.client.ImageAsset.Delete().
		Where(
			imageasset.IDEQ(id),
			imageasset.UserIDEQ(userID),
		).
		Exec(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrImageAssetNotFound
	}
	return nil
}

// SoftDeleteByGeneration 软删除某次生成任务的所有资产。
// 用于级联删除：当用户删除一个 generation 时，同步软删除其所有 input/output 图片资产。
func (r *imageAssetRepository) SoftDeleteByGeneration(ctx context.Context, userID, generationID int64) error {
	_, err := r.client.ImageAsset.Delete().
		Where(
			imageasset.UserIDEQ(userID),
			imageasset.GenerationIDEQ(generationID),
		).
		Exec(ctx)
	return err
}

// ==================== 资产清理（管理员/后台调度专用）====================
//
// 以下方法跨用户操作，仅用于清理已软删除但存储对象仍存在的孤立资产。
// 必须在 mixins.SkipSoftDelete(ctx) 下调用，否则软删除拦截器会过滤掉这些记录。

// ListSoftDeletedBefore 列出 deleted_at < cutoff 的已软删除资产（跨用户）。
// 按 deleted_at 升序，限制 limit 条，便于分批清理。
func (r *imageAssetRepository) ListSoftDeletedBefore(ctx context.Context, cutoff time.Time, limit int) ([]*service.ImageAsset, error) {
	if limit <= 0 {
		limit = 100
	}
	items, err := r.client.ImageAsset.Query().
		Where(
			imageasset.DeletedAtNotNil(),
			imageasset.DeletedAtLT(cutoff),
		).
		Order(ent.Asc(imageasset.FieldDeletedAt), ent.Asc(imageasset.FieldID)).
		Limit(limit).
		All(mixins.SkipSoftDelete(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]*service.ImageAsset, 0, len(items))
	for _, m := range items {
		out = append(out, assetToService(m))
	}
	return out, nil
}

// CountSoftDeletedBefore 统计 deleted_at < cutoff 的已软删除资产数量（用于清理预览）。
func (r *imageAssetRepository) CountSoftDeletedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	count, err := r.client.ImageAsset.Query().
		Where(
			imageasset.DeletedAtNotNil(),
			imageasset.DeletedAtLT(cutoff),
		).
		Count(mixins.SkipSoftDelete(ctx))
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

// HardDelete 物理删除资产记录。仅在存储对象已删除后调用，避免出现新的孤立记录。
func (r *imageAssetRepository) HardDelete(ctx context.Context, id int64) error {
	affected, err := r.client.ImageAsset.Delete().
		Where(imageasset.IDEQ(id)).
		Exec(mixins.SkipSoftDelete(ctx))
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrImageAssetNotFound
	}
	return nil
}

// assetToService 把 ent 模型映射到 service 层。
func assetToService(m *ent.ImageAsset) *service.ImageAsset {
	return &service.ImageAsset{
		ID:               m.ID,
		UserID:           m.UserID,
		GenerationID:     m.GenerationID,
		AssetType:        string(m.AssetType),
		S3Bucket:         m.S3Bucket,
		S3Key:            m.S3Key,
		MimeType:         m.MimeType,
		FileSize:         m.FileSize,
		Width:            m.Width,
		Height:           m.Height,
		SHA256:           m.Sha256,
		OriginalFilename: m.OriginalFilename,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
		DeletedAt:        m.DeletedAt,
	}
}
