package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/imagegeneration"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type imageGenerationRepository struct {
	client *ent.Client
	db     *sql.DB // 用于 advisory lock 和原生 SQL（CAS claim）
}

// NewImageGenerationRepository 构造图片生成任务仓库。
// db 用于 pg_advisory_xact_lock（用户级并发检查）和 ClaimQueued CAS 更新。
// 所有面向用户的查询强制附带 user_id 条件，避免跨用户越权访问。
// GetByID 和 ListStaleProcessing 是内部系统操作（恢复、排障），不附带 user_id。
func NewImageGenerationRepository(client *ent.Client, db *sql.DB) service.ImageGenerationRepository {
	return &imageGenerationRepository{client: client, db: db}
}

func (r *imageGenerationRepository) Create(ctx context.Context, params service.CreateImageGenerationParams) (*service.ImageGeneration, error) {
	b := r.client.ImageGeneration.Create().
		SetUserID(params.UserID).
		SetConversationID(params.ConversationID).
		SetProvider(params.Provider).
		SetModel(params.Model).
		SetGenerationType(imagegeneration.GenerationType(params.GenerationType)).
		SetPrompt(params.Prompt).
		SetSize(params.Size).
		SetRatio(params.Ratio).
		SetStatus(imagegeneration.Status(params.Status))
	if params.ParentGenerationID != nil {
		b.SetParentGenerationID(*params.ParentGenerationID)
	}
	if params.IdempotencyKey != nil && *params.IdempotencyKey != "" {
		b.SetIdempotencyKey(*params.IdempotencyKey)
	}
	m, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return generationToService(m), nil
}

func (r *imageGenerationRepository) GetByIDForOwner(ctx context.Context, userID, id int64) (*service.ImageGeneration, error) {
	m, err := r.client.ImageGeneration.Query().
		Where(
			imagegeneration.IDEQ(id),
			imagegeneration.UserIDEQ(userID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrImageGenerationNotFound
		}
		return nil, err
	}
	return generationToService(m), nil
}

// GetByID 不附带 user_id 过滤，仅供内部系统操作使用（如卡死任务恢复）。
// 调用方必须确保不将结果直接返回给用户。
func (r *imageGenerationRepository) GetByID(ctx context.Context, id int64) (*service.ImageGeneration, error) {
	m, err := r.client.ImageGeneration.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrImageGenerationNotFound
		}
		return nil, err
	}
	return generationToService(m), nil
}

func (r *imageGenerationRepository) GetByIdempotencyKey(ctx context.Context, userID int64, key string) (*service.ImageGeneration, error) {
	if key == "" {
		return nil, service.ErrImageGenerationNotFound
	}
	m, err := r.client.ImageGeneration.Query().
		Where(
			imagegeneration.UserIDEQ(userID),
			imagegeneration.IdempotencyKeyEQ(key),
		).
		Order(ent.Desc(imagegeneration.FieldID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrImageGenerationNotFound
		}
		return nil, err
	}
	return generationToService(m), nil
}

func (r *imageGenerationRepository) List(ctx context.Context, filter service.ImageGenerationFilter) (*service.ImageGenerationList, error) {
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	q := r.client.ImageGeneration.Query().Where(imagegeneration.UserIDEQ(filter.UserID))
	if filter.ConversationID != nil {
		q = q.Where(imagegeneration.ConversationIDEQ(*filter.ConversationID))
	}
	if filter.Status != "" {
		q = q.Where(imagegeneration.StatusEQ(imagegeneration.Status(filter.Status)))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, err
	}

	items, err := q.
		Order(ent.Desc(imagegeneration.FieldCreatedAt), ent.Desc(imagegeneration.FieldID)).
		Offset(offset).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*service.ImageGeneration, 0, len(items))
	for _, m := range items {
		out = append(out, generationToService(m))
	}
	return &service.ImageGenerationList{
		Items:    out,
		Total:    int64(total),
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (r *imageGenerationRepository) ListByConversation(ctx context.Context, userID, conversationID int64) ([]*service.ImageGeneration, error) {
	items, err := r.client.ImageGeneration.Query().
		Where(
			imagegeneration.UserIDEQ(userID),
			imagegeneration.ConversationIDEQ(conversationID),
		).
		Order(ent.Asc(imagegeneration.FieldCreatedAt), ent.Asc(imagegeneration.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.ImageGeneration, 0, len(items))
	for _, m := range items {
		out = append(out, generationToService(m))
	}
	return out, nil
}

func (r *imageGenerationRepository) UpdateStatus(ctx context.Context, userID, id int64, params service.UpdateImageGenerationStatusParams) (*service.ImageGeneration, error) {
	b := r.client.ImageGeneration.Update().
		Where(
			imagegeneration.IDEQ(id),
			imagegeneration.UserIDEQ(userID),
		)
	if params.Status != nil {
		b.SetStatus(imagegeneration.Status(*params.Status))
	}
	if params.ProviderCredentialID != nil {
		b.SetProviderCredentialID(*params.ProviderCredentialID)
	}
	if params.ProviderRequestID != nil {
		b.SetProviderRequestID(*params.ProviderRequestID)
	}
	if params.ProviderOriginalURL != nil {
		b.SetProviderOriginalURL(*params.ProviderOriginalURL)
	}
	if params.ErrorCode != nil {
		b.SetErrorCode(*params.ErrorCode)
	}
	if params.ErrorMessage != nil {
		b.SetErrorMessage(*params.ErrorMessage)
	}
	if params.DurationMs != nil {
		b.SetDurationMs(*params.DurationMs)
	}
	if params.StartedAt != nil {
		b.SetStartedAt(*params.StartedAt)
	}
	if params.CompletedAt != nil {
		b.SetCompletedAt(*params.CompletedAt)
	}
	affected, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, service.ErrImageGenerationNotFound
	}
	return r.GetByIDForOwner(ctx, userID, id)
}

// ListStaleProcessing 列出卡在 processing 且 started_at 早于 cutoff 的任务。
// 不附带 user_id 过滤——这是内部系统恢复操作，调用方不将结果返回给用户。
// started_at 为 NULL 的 processing 任务也视为卡死（异常状态）。
func (r *imageGenerationRepository) ListStaleProcessing(ctx context.Context, filter service.StaleProcessingFilter) ([]*service.ImageGeneration, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := r.client.ImageGeneration.Query().
		Where(
			imagegeneration.StatusEQ(imagegeneration.StatusProcessing),
			imagegeneration.Or(
				imagegeneration.StartedAtLTE(filter.BeforeTime),
				imagegeneration.StartedAtIsNil(),
			),
		).
		Order(ent.Asc(imagegeneration.FieldStartedAt), ent.Asc(imagegeneration.FieldID)).
		Limit(limit)
	items, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.ImageGeneration, 0, len(items))
	for _, m := range items {
		out = append(out, generationToService(m))
	}
	return out, nil
}

func (r *imageGenerationRepository) SoftDelete(ctx context.Context, userID, id int64) error {
	// ImageGeneration 没有使用 SoftDeleteMixin，这里做物理删除。
	// 但为了保留历史引用（计费、审计），service 层通常不会真正删除 generation 记录，
	// 而是标记 status=canceled 或保留记录。此方法仅在用户显式删除时调用。
	affected, err := r.client.ImageGeneration.Delete().
		Where(
			imagegeneration.IDEQ(id),
			imagegeneration.UserIDEQ(userID),
		).
		Exec(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrImageGenerationNotFound
	}
	return nil
}

// CreateIfUnderUserConcurrency 在事务内原子地检查用户活跃任务数并创建新任务。
//
// 使用 pg_advisory_xact_lock(user_id) 序列化同一用户的并发创建请求，
// 在同一事务内 count(pending+queued+processing) + insert，避免竞态。
// 当活跃任务数 >= maxConcurrent 时返回 ErrImageConcurrentLimitReached。
// maxConcurrent <= 0 时退化为普通 Create（不检查并发）。
//
// 非 PostgreSQL 环境下退化为普通 Create（跳过 advisory lock）。
func (r *imageGenerationRepository) CreateIfUnderUserConcurrency(ctx context.Context, params service.CreateImageGenerationParams, maxConcurrent int) (*service.ImageGeneration, error) {
	// maxConcurrent <= 0：不启用并发检查，退化为普通 Create
	if maxConcurrent <= 0 {
		return r.Create(ctx, params)
	}

	// 非 PostgreSQL 或无 sql.DB：退化为普通 Create（无法做 advisory lock）
	if r.db == nil || r.client.Driver().Dialect() != "postgres" {
		return r.Create(ctx, params)
	}

	// 使用底层 sql.DB 开启事务，在同一事务内执行 advisory lock + count + insert
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx for concurrency check: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// pg_advisory_xact_lock 在事务结束时自动释放，序列化同一用户的并发创建
	// 使用 user_id 作为 lock key（int64 直接可用，无需 hash）
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", params.UserID); err != nil {
		return nil, fmt.Errorf("acquire advisory lock for user %d: %w", params.UserID, err)
	}

	// 在锁内 count 活跃任务（pending + queued + processing）
	// 使用软删除过滤（image_generations 表无软删除，但保险起见保持一致）
	var activeCount int
	countQuery := `SELECT COUNT(*) FROM image_generations WHERE user_id = $1 AND status IN ('pending', 'queued', 'processing')`
	if err := tx.QueryRowContext(ctx, countQuery, params.UserID).Scan(&activeCount); err != nil {
		return nil, fmt.Errorf("count active generations for user %d: %w", params.UserID, err)
	}

	if activeCount >= maxConcurrent {
		return nil, service.ErrImageConcurrentLimitReached
	}

	// 在同一事务内插入新任务
	insertQuery := `INSERT INTO image_generations (user_id, conversation_id, parent_generation_id, provider, model, generation_type, prompt, size, ratio, status, idempotency_key, created_at, updated_at, duration_ms) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW(), 0) RETURNING id`
	var id int64
	var idemKey sql.NullString
	if params.IdempotencyKey != nil && *params.IdempotencyKey != "" {
		idemKey = sql.NullString{String: *params.IdempotencyKey, Valid: true}
	}
	// parent_generation_id 是 int64，需要用 NullInt64
	var parentID sql.NullInt64
	if params.ParentGenerationID != nil {
		parentID = sql.NullInt64{Int64: *params.ParentGenerationID, Valid: true}
	}
	row := tx.QueryRowContext(ctx, insertQuery,
		params.UserID, params.ConversationID, parentID, params.Provider, params.Model,
		params.GenerationType, params.Prompt, params.Size, params.Ratio, params.Status, idemKey,
	)
	if err := row.Scan(&id); err != nil {
		return nil, fmt.Errorf("insert generation: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit concurrency check tx: %w", err)
	}

	// 重新通过 ent 查询完整记录（包含默认值、触发器生成的字段）
	created, err := r.client.ImageGeneration.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return generationToService(created), nil
}

// CountActiveByUser 统计用户当前活跃（pending + queued + processing）的生图任务数。
// 供管理员查看和调试使用。
func (r *imageGenerationRepository) CountActiveByUser(ctx context.Context, userID int64) (int, error) {
	count, err := r.client.ImageGeneration.Query().
		Where(
			imagegeneration.UserIDEQ(userID),
			imagegeneration.StatusIn(
				imagegeneration.StatusPending,
				imagegeneration.StatusQueued,
				imagegeneration.StatusProcessing,
			),
		).
		Count(ctx)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// ListQueued 列出 queued 状态的任务（按 created_at 升序，FIFO），供 dispatcher 调度。
// 不附带 user_id 过滤——这是内部系统调度操作，调用方不将结果返回给用户。
func (r *imageGenerationRepository) ListQueued(ctx context.Context, limit int) ([]*service.ImageGeneration, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	// 跳过软删除过滤（image_generations 表无软删除，但保持一致性）
	ctx = mixins.SkipSoftDelete(ctx)
	items, err := r.client.ImageGeneration.Query().
		Where(imagegeneration.StatusEQ(imagegeneration.StatusQueued)).
		Order(ent.Asc(imagegeneration.FieldCreatedAt), ent.Asc(imagegeneration.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.ImageGeneration, 0, len(items))
	for _, m := range items {
		out = append(out, generationToService(m))
	}
	return out, nil
}

// ClaimQueued 原子地将任务从 queued 状态更新为 processing（CAS）。
// 使用 UPDATE ... WHERE id = $1 AND status = 'queued' 防止多节点重复调度同一任务。
// 返回 (true, nil) 表示成功认领；返回 (false, nil) 表示任务已被其他流程认领或状态已变更。
// startedAt 设为当前时间，用于卡死任务恢复判断。
func (r *imageGenerationRepository) ClaimQueued(ctx context.Context, taskID int64) (bool, error) {
	// 使用原生 SQL 做 CAS 更新，确保原子性
	query := `UPDATE image_generations SET status = 'processing', started_at = NOW(), updated_at = NOW() WHERE id = $1 AND status = 'queued'`
	result, err := r.db.ExecContext(ctx, query, taskID)
	if err != nil {
		return false, fmt.Errorf("claim queued task %d: %w", taskID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check rows affected for claim %d: %w", taskID, err)
	}
	return affected > 0, nil
}

// RevertToQueued 将 processing 状态的任务回退为 queued（用于凭据获取失败时恢复）。
// 这是一个补偿操作：任务已标记为 processing 但实际上无法继续执行（如所有凭据都忙）。
// 清除 started_at 以避免被 RecoverStaleGenerations 误判为卡死任务。
func (r *imageGenerationRepository) RevertToQueued(ctx context.Context, taskID int64) error {
	query := `UPDATE image_generations SET status = 'queued', started_at = NULL, updated_at = NOW() WHERE id = $1 AND status = 'processing'`
	_, err := r.db.ExecContext(ctx, query, taskID)
	if err != nil {
		return fmt.Errorf("revert task %d to queued: %w", taskID, err)
	}
	return nil
}

// generationToService 把 ent 模型映射到 service 层。
func generationToService(m *ent.ImageGeneration) *service.ImageGeneration {
	return &service.ImageGeneration{
		ID:                   m.ID,
		UserID:               m.UserID,
		ConversationID:       m.ConversationID,
		ParentGenerationID:   m.ParentGenerationID,
		Provider:             m.Provider,
		ProviderCredentialID: m.ProviderCredentialID,
		Model:                m.Model,
		GenerationType:       string(m.GenerationType),
		Prompt:               m.Prompt,
		Size:                 m.Size,
		Ratio:                m.Ratio,
		Status:               string(m.Status),
		IdempotencyKey:       m.IdempotencyKey,
		ProviderRequestID:    m.ProviderRequestID,
		ProviderOriginalURL:  m.ProviderOriginalURL,
		ErrorCode:            m.ErrorCode,
		ErrorMessage:         m.ErrorMessage,
		DurationMs:           m.DurationMs,
		StartedAt:            m.StartedAt,
		CompletedAt:          m.CompletedAt,
		CreatedAt:            m.CreatedAt,
		UpdatedAt:            m.UpdatedAt,
	}
}
