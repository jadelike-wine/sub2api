package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// 图片资产清理相关常量。
const (
	// imageAssetCleanupJobName heartbeat 表中的任务名。
	imageAssetCleanupJobName = "image_asset_cleanup"
	// imageAssetCleanupLeaderLockKey Redis leader lock 键。
	imageAssetCleanupLeaderLockKeyDefault = "image:asset_cleanup:leader"
	// imageAssetCleanupLeaderLockTTLDefault Redis leader lock TTL。
	imageAssetCleanupLeaderLockTTLDefault = 10 * time.Minute
	// imageAssetCleanupRunTimeout 单次清理运行超时。
	imageAssetCleanupRunTimeout = 30 * time.Minute
	// imageAssetCleanupHeartbeatTimeout heartbeat 写入超时。
	imageAssetCleanupHeartbeatTimeout = 2 * time.Second

	// 默认配置（当 config 字段未设置时使用）。
	defaultImageAssetCleanupRetentionDays = 7
	defaultImageAssetCleanupInterval      = 60 * time.Minute
	defaultImageAssetCleanupBatchSize      = 100
	// 单次手动清理最多处理的资产数（防止一次清理拖垮存储）。
	maxImageAssetManualCleanupBatch = 5000
)

// imageAssetCleanupReleaseScript 与 ops_cleanup 一致的释放脚本：仅持有者可释放。
var imageAssetCleanupReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// ImageAssetCleanupService 清理"已软删除但存储对象仍存在"的孤立图片资产。
//
// 设计要点：
//   - 定时扫描：ticker 按 AssetCleanupIntervalMinutes 间隔触发，仅 leader 节点执行
//     （Redis SetNX → DB advisory lock 降级，与 OpsCleanupService 一致）
//   - 一键清理：管理员通过 HTTP 触发 RunOnce，无需 leader lock（操作幂等：
//     storage.Delete 删除不存在的对象返回 nil；HardDelete 用 ID 过滤，重复执行 0 影响）
//   - 容错：单个资产清理失败（存储删除失败）不阻塞整体流程，该记录保留待下次重试
//   - 顺序：先 storage.Delete 成功 → 再 HardDelete DB 记录，避免出现"DB 删了但文件残留"
//   - 配置驱动：retention / interval / batch_size 均来自 config.ImageGenerationConfig
//
// 安全约束：
//   - 仅清理已软删除（deleted_at IS NOT NULL）且 deleted_at < cutoff 的资产
//   - 不会触碰任何未软删除的活跃资产，避免误删用户正在使用的图片
type ImageAssetCleanupService struct {
	assetRepo ImageAssetRepository
	storage   ImageObjectStorage
	opsRepo   OpsRepository // 可选，用于记录 heartbeat；为 nil 时跳过
	db        *sql.DB       // 可选，用于 DB advisory lock 降级
	redis     *redis.Client // 可选，用于 Redis leader lock
	cfg       *config.Config

	instanceID string

	// ticker 生命周期守护。
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewImageAssetCleanupService 构造清理服务。
func NewImageAssetCleanupService(
	assetRepo ImageAssetRepository,
	storage ImageObjectStorage,
	opsRepo OpsRepository,
	db *sql.DB,
	redisClient *redis.Client,
	cfg *config.Config,
) *ImageAssetCleanupService {
	return &ImageAssetCleanupService{
		assetRepo:  assetRepo,
		storage:    storage,
		opsRepo:    opsRepo,
		db:         db,
		redis:      redisClient,
		cfg:        cfg,
		instanceID: uuid.NewString(),
	}
}

// CleanupParams 清理参数。
// OlderThanDays 与 BeforeDate 二选一：
//   - OlderThanDays 非 nil：cutoff = now - N 天
//   - BeforeDate 非 nil：cutoff = BeforeDate（精确到秒）
//   - 两者都为 nil：使用配置中的 retention_days
type CleanupParams struct {
	OlderThanDays *int
	BeforeDate    *time.Time
	BatchSize     int
	// Reason 标识清理来源，用于日志/heartbeat（"scheduled" / "manual"）。
	Reason string
}

// CleanupResult 清理结果统计。
type CleanupResult struct {
	Scanned              int   `json:"scanned"`
	DeletedAssets        int   `json:"deleted_assets"`
	DeletedStorageObjects int  `json:"deleted_storage_objects"`
	StorageFailures      int   `json:"storage_failures"`
	DBFailures           int   `json:"db_failures"`
	DurationMs           int64 `json:"duration_ms"`
	Cutoff               time.Time `json:"cutoff"`
}

// Start 启动定时清理 ticker。幂等：重复调用安全。
// 仅当 ImageGeneration.Enabled 且 AssetCleanupEnabled 为 true 时启动。
func (s *ImageAssetCleanupService) Start() {
	if s == nil || s.assetRepo == nil || s.storage == nil || s.cfg == nil {
		return
	}
	if !s.cfg.ImageGeneration.Enabled || !s.cleanupEnabled() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.runLoop(ctx)
	slog.Info("image asset cleanup: scheduler started",
		"interval", s.cleanupInterval().String(),
		"retention_days", s.cleanupRetentionDays(),
		"batch_size", s.cleanupBatchSize())
}

// Stop 停止 ticker 并等待退出。幂等。
func (s *ImageAssetCleanupService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// runLoop 定时清理循环。每次触发尝试获取 leader lock，成功则执行 RunOnce。
func (s *ImageAssetCleanupService) runLoop(ctx context.Context) {
	defer close(s.done)
	interval := s.cleanupInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.runScheduled(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runScheduled 定时触发入口：带 leader lock + heartbeat。
func (s *ImageAssetCleanupService) runScheduled(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, imageAssetCleanupRunTimeout)
	defer cancel()

	release, ok := s.tryAcquireLeaderLock(runCtx)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}

	startedAt := time.Now()
	result, err := s.RunOnce(runCtx, CleanupParams{Reason: "scheduled"})
	duration := time.Since(startedAt)
	if err != nil {
		s.recordHeartbeatError(startedAt, duration, err)
		slog.Error("image asset cleanup: scheduled run failed", "error", err, "duration_ms", duration.Milliseconds())
		return
	}
	s.recordHeartbeatSuccess(startedAt, duration, result)
	slog.Info("image asset cleanup: scheduled run complete",
		"scanned", result.Scanned,
		"deleted_assets", result.DeletedAssets,
		"storage_failures", result.StorageFailures,
		"duration_ms", result.DurationMs)
}

// RunOnce 执行一次清理。定时任务和管理员一键清理共用。
//
// 流程：
//  1. 计算 cutoff
//  2. 分批拉取已软删除且 deleted_at < cutoff 的资产
//  3. 对每个资产：storage.Delete → HardDelete（失败则跳过 DB 删除，保留记录待重试）
//  4. 返回统计
//
// 该方法幂等，可安全并发调用（但建议定时任务通过 leader lock 串行化）。
func (s *ImageAssetCleanupService) RunOnce(ctx context.Context, params CleanupParams) (CleanupResult, error) {
	out := CleanupResult{}
	if s == nil || s.assetRepo == nil {
		return out, errors.New("image asset cleanup service not initialized")
	}
	startedAt := time.Now()
	out.Cutoff = s.computeCutoff(params)
	batchSize := params.BatchSize
	if batchSize <= 0 {
		batchSize = s.cleanupBatchSize()
	}
	if batchSize > maxImageAssetManualCleanupBatch {
		batchSize = maxImageAssetManualCleanupBatch
	}

	for {
		if ctx.Err() != nil {
			out.DurationMs = time.Since(startedAt).Milliseconds()
			return out, ctx.Err()
		}
		assets, err := s.assetRepo.ListSoftDeletedBefore(ctx, out.Cutoff, batchSize)
		if err != nil {
			out.DurationMs = time.Since(startedAt).Milliseconds()
			return out, fmt.Errorf("list soft-deleted assets: %w", err)
		}
		if len(assets) == 0 {
			break
		}
		out.Scanned += len(assets)
		for _, a := range assets {
			s.cleanupOne(ctx, a, &out)
		}
		// 若本批未满 batchSize，说明已无更多待清理记录
		if len(assets) < batchSize {
			break
		}
	}
	out.DurationMs = time.Since(startedAt).Milliseconds()
	return out, nil
}

// PreviewCleanup 预览将要清理的资产数量（不执行删除）。
// 供管理员在前端确认清理范围时调用。
func (s *ImageAssetCleanupService) PreviewCleanup(ctx context.Context, params CleanupParams) (int64, error) {
	if s == nil || s.assetRepo == nil {
		return 0, errors.New("image asset cleanup service not initialized")
	}
	cutoff := s.computeCutoff(params)
	return s.assetRepo.CountSoftDeletedBefore(ctx, cutoff)
}

// cleanupOne 清理单个资产：先删存储对象，再物理删除 DB 记录。
// 存储删除失败时保留 DB 记录，下次清理重试。
func (s *ImageAssetCleanupService) cleanupOne(ctx context.Context, a *ImageAsset, out *CleanupResult) {
	if a == nil || a.S3Key == "" {
		// 无 S3 Key 的孤儿记录直接物理删除
		if err := s.assetRepo.HardDelete(ctx, a.ID); err != nil {
			out.DBFailures++
			slog.Warn("image asset cleanup: hard delete orphan record failed",
				"asset_id", a.ID, "error", err)
			return
		}
		out.DeletedAssets++
		return
	}

	if err := s.storage.Delete(ctx, a.S3Key); err != nil {
		out.StorageFailures++
		slog.Warn("image asset cleanup: storage delete failed, keep DB record for retry",
			"asset_id", a.ID, "s3_key", a.S3Key, "error", err)
		return
	}
	out.DeletedStorageObjects++

	if err := s.assetRepo.HardDelete(ctx, a.ID); err != nil {
		out.DBFailures++
		// DB 删除失败但存储已删，记录为孤儿（存储无对象、DB 有记录），下次清理会再次尝试
		slog.Warn("image asset cleanup: hard delete DB record failed after storage delete",
			"asset_id", a.ID, "error", err)
		return
	}
	out.DeletedAssets++
}

// computeCutoff 根据参数计算清理截止时间。
func (s *ImageAssetCleanupService) computeCutoff(params CleanupParams) time.Time {
	if params.BeforeDate != nil {
		return *params.BeforeDate
	}
	if params.OlderThanDays != nil && *params.OlderThanDays > 0 {
		return time.Now().Add(-time.Duration(*params.OlderThanDays) * 24 * time.Hour)
	}
	return time.Now().Add(-time.Duration(s.cleanupRetentionDays()) * 24 * time.Hour)
}

// ==================== Leader Lock ====================

// tryAcquireLeaderLock 尝试获取 leader lock。
// simple 模式直接放行；集群模式优先 Redis SetNX，失败降级 DB advisory lock。
func (s *ImageAssetCleanupService) tryAcquireLeaderLock(ctx context.Context) (func(), bool) {
	if s == nil {
		return nil, false
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil, true
	}
	key := imageAssetCleanupLeaderLockKeyDefault
	ttl := imageAssetCleanupLeaderLockTTLDefault

	if s.redis != nil {
		ok, err := s.redis.SetNX(ctx, key, s.instanceID, ttl).Result()
		if err == nil {
			if !ok {
				return nil, false
			}
			return func() {
				_, _ = imageAssetCleanupReleaseScript.Run(ctx, s.redis, []string{key}, s.instanceID).Result()
			}, true
		}
		slog.Warn("image asset cleanup: redis leader lock failed, falling back to DB advisory lock", "error", err)
	}
	if s.db != nil {
		release, ok := tryAcquireDBAdvisoryLock(ctx, s.db, hashAdvisoryLockID(key))
		return release, ok
	}
	// 既无 Redis 也无 DB，单实例兜底放行
	return nil, true
}

// ==================== Heartbeat ====================

func (s *ImageAssetCleanupService) recordHeartbeatSuccess(runAt time.Time, duration time.Duration, result CleanupResult) {
	if s == nil || s.opsRepo == nil {
		return
	}
	now := time.Now().UTC()
	durMs := duration.Milliseconds()
	summary := truncateString(fmt.Sprintf("scanned=%d deleted_assets=%d storage_failures=%d db_failures=%d cutoff=%s",
		result.Scanned, result.DeletedAssets, result.StorageFailures, result.DBFailures,
		result.Cutoff.Format(time.RFC3339)), 2048)
	ctx, cancel := context.WithTimeout(context.Background(), imageAssetCleanupHeartbeatTimeout)
	defer cancel()
	_ = s.opsRepo.UpsertJobHeartbeat(ctx, &OpsUpsertJobHeartbeatInput{
		JobName:        imageAssetCleanupJobName,
		LastRunAt:      &runAt,
		LastSuccessAt:  &now,
		LastDurationMs: &durMs,
		LastResult:     &summary,
	})
}

func (s *ImageAssetCleanupService) recordHeartbeatError(runAt time.Time, duration time.Duration, err error) {
	if s == nil || s.opsRepo == nil || err == nil {
		return
	}
	now := time.Now().UTC()
	durMs := duration.Milliseconds()
	msg := truncateString(err.Error(), 2048)
	ctx, cancel := context.WithTimeout(context.Background(), imageAssetCleanupHeartbeatTimeout)
	defer cancel()
	_ = s.opsRepo.UpsertJobHeartbeat(ctx, &OpsUpsertJobHeartbeatInput{
		JobName:        imageAssetCleanupJobName,
		LastRunAt:      &runAt,
		LastErrorAt:    &now,
		LastError:      &msg,
		LastDurationMs: &durMs,
	})
}

// ==================== 配置读取 ====================

func (s *ImageAssetCleanupService) cleanupEnabled() bool {
	if s.cfg == nil {
		return false
	}
	// 未显式配置时默认启用（清理是运维必要功能，且默认配置安全：只清已软删除 + 7 天保留）
	return s.cfg.ImageGeneration.AssetCleanupEnabled
}

func (s *ImageAssetCleanupService) cleanupRetentionDays() int {
	if s.cfg != nil && s.cfg.ImageGeneration.AssetCleanupRetentionDays > 0 {
		return s.cfg.ImageGeneration.AssetCleanupRetentionDays
	}
	return defaultImageAssetCleanupRetentionDays
}

func (s *ImageAssetCleanupService) cleanupInterval() time.Duration {
	if s.cfg != nil && s.cfg.ImageGeneration.AssetCleanupIntervalMinutes > 0 {
		return time.Duration(s.cfg.ImageGeneration.AssetCleanupIntervalMinutes) * time.Minute
	}
	return defaultImageAssetCleanupInterval
}

func (s *ImageAssetCleanupService) cleanupBatchSize() int {
	if s.cfg != nil && s.cfg.ImageGeneration.AssetCleanupBatchSize > 0 {
		return s.cfg.ImageGeneration.AssetCleanupBatchSize
	}
	return defaultImageAssetCleanupBatchSize
}

// ValidateCleanupParams 校验手动清理参数（管理员一键清理用）。
// 返回脱敏后的错误描述，避免泄露内部细节。
func ValidateCleanupParams(params CleanupParams) error {
	if params.OlderThanDays != nil && *params.OlderThanDays < 0 {
		return errors.New("older_than_days must be >= 0")
	}
	if params.BeforeDate != nil && params.BeforeDate.After(time.Now()) {
		return errors.New("before_date must not be in the future")
	}
	if params.OlderThanDays != nil && params.BeforeDate != nil {
		return errors.New("older_than_days and before_date are mutually exclusive")
	}
	return nil
}
