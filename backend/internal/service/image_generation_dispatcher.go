package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// 图片生成调度器（queued 任务分发）相关常量。
const (
	// imageGenDispatcherJobName heartbeat 表中的任务名。
	imageGenDispatcherJobName = "image_generation_dispatcher"
	// imageGenDispatcherLeaderLockKeyDefault Redis leader lock 键。
	imageGenDispatcherLeaderLockKeyDefault = "image:generation_dispatcher:leader"
	// imageGenDispatcherLeaderLockTTLDefault Redis leader lock TTL。
	imageGenDispatcherLeaderLockTTLDefault = 2 * time.Minute
	// imageGenDispatcherRunTimeout 单次调度运行超时。
	imageGenDispatcherRunTimeout = 5 * time.Minute
	// imageGenDispatcherHeartbeatTimeout heartbeat 写入超时。
	imageGenDispatcherHeartbeatTimeout = 2 * time.Second
	// imageGenDispatcherBatchSize 单次扫描的 queued 任务数上限。
	imageGenDispatcherBatchSize = 20
)

// imageGenDispatcherReleaseScript 与 imageAssetCleanupReleaseScript 一致：仅持有者可释放。
var imageGenDispatcherReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// ImageGenerationDispatcher 扫描 queued 状态的生图任务并触发调度。
//
// 设计要点：
//   - 定时扫描：ticker 按 RecoveryIntervalSeconds 间隔触发，仅 leader 节点执行
//     （Redis SetNX → DB advisory lock 降级，与 ImageAssetCleanupService 一致）
//   - 服务重启恢复：启动时立即执行一次，扫描 queued 任务并尝试调度
//     + 调用 RecoverStaleGenerations 处理卡死 processing 任务
//   - 任务实际执行在独立 goroutine 中（由 ImageGenerationService.tryDispatch 负责），
//     dispatcher 仅负责触发调度，不阻塞 ticker
//   - 配置驱动：interval 来自 config.ImageGenerationConfig.RecoveryIntervalSeconds
//
// 与 ImageGenerationService.tryDispatch 的协作：
//   - CreateGeneration 创建任务后会立即异步调用 tryDispatch（无需等待 ticker）
//   - 当所有 Token 忙碌时，tryDispatch 会将任务回退为 queued，由本 dispatcher 后续调度
//   - dispatcher ticker 触发时调用 DispatchQueuedBatch 批量调度
type ImageGenerationDispatcher struct {
	genService *ImageGenerationService
	opsRepo    OpsRepository // 可选，用于记录 heartbeat；为 nil 时跳过
	db         *sql.DB       // 可选，用于 DB advisory lock 降级
	redis      *redis.Client // 可选，用于 Redis leader lock
	cfg        *config.Config

	instanceID string

	// ticker 生命周期守护。
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewImageGenerationDispatcher 构造调度器。
func NewImageGenerationDispatcher(
	genService *ImageGenerationService,
	opsRepo OpsRepository,
	db *sql.DB,
	redisClient *redis.Client,
	cfg *config.Config,
) *ImageGenerationDispatcher {
	return &ImageGenerationDispatcher{
		genService: genService,
		opsRepo:    opsRepo,
		db:         db,
		redis:      redisClient,
		cfg:        cfg,
		instanceID: uuid.NewString(),
	}
}

// Start 启动定时调度 ticker。幂等：重复调用安全。
// 仅当 ImageGeneration.Enabled 为 true 时启动。
// 启动时立即执行一次扫描（恢复服务重启前遗留的 queued 任务）。
func (s *ImageGenerationDispatcher) Start() {
	if s == nil || s.genService == nil || s.cfg == nil {
		return
	}
	if !s.cfg.ImageGeneration.Enabled {
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
	slog.Info("image generation dispatcher: started",
		"interval", s.dispatchInterval().String())
}

// Stop 停止 ticker 并等待退出。幂等。
func (s *ImageGenerationDispatcher) Stop() {
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

// runLoop 定时调度循环。每次触发尝试获取 leader lock，成功则执行调度。
func (s *ImageGenerationDispatcher) runLoop(ctx context.Context) {
	defer close(s.done)

	// 启动时立即执行一次：恢复服务重启前遗留的 queued 任务 + 清理卡死 processing 任务
	s.runScheduled(ctx)

	interval := s.dispatchInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runScheduled(ctx)
		}
	}
}

// runScheduled 定时触发入口：带 leader lock + heartbeat。
func (s *ImageGenerationDispatcher) runScheduled(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, imageGenDispatcherRunTimeout)
	defer cancel()

	release, ok := s.tryAcquireLeaderLock(runCtx)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}

	startedAt := time.Now()
	result, err := s.runOnce(runCtx)
	duration := time.Since(startedAt)
	if err != nil {
		s.recordHeartbeatError(startedAt, duration, err)
		slog.Error("image generation dispatcher: scheduled run failed",
			"error", err, "duration_ms", duration.Milliseconds())
		return
	}
	s.recordHeartbeatSuccess(startedAt, duration, result)
	slog.Info("image generation dispatcher: scheduled run complete",
		"dispatched", result.Dispatched,
		"recovered_stale", result.RecoveredStale,
		"duration_ms", result.DurationMs)
}

// DispatcherRunResult 单次调度运行结果。
type DispatcherRunResult struct {
	Dispatched     int   `json:"dispatched"`
	RecoveredStale int   `json:"recovered_stale"`
	DurationMs     int64 `json:"duration_ms"`
}

// runOnce 执行一次调度：恢复卡死任务 + 分发 queued 任务。
func (s *ImageGenerationDispatcher) runOnce(ctx context.Context) (DispatcherRunResult, error) {
	out := DispatcherRunResult{}
	startedAt := time.Now()

	// 1. 恢复卡死 processing 任务（服务崩溃/重启遗留）
	recovered, err := s.genService.RecoverStaleGenerations(ctx)
	if err != nil {
		slog.Warn("image generation dispatcher: RecoverStaleGenerations failed",
			"error", err)
		// 不中断流程，继续调度 queued 任务
	}
	out.RecoveredStale = recovered

	// 2. 分发 queued 任务
	dispatched, err := s.genService.DispatchQueuedBatch(ctx, imageGenDispatcherBatchSize)
	if err != nil {
		out.DurationMs = time.Since(startedAt).Milliseconds()
		return out, err
	}
	out.Dispatched = dispatched
	out.DurationMs = time.Since(startedAt).Milliseconds()
	return out, nil
}

// dispatchInterval 返回调度间隔。优先使用 config，否则默认 60s。
func (s *ImageGenerationDispatcher) dispatchInterval() time.Duration {
	seconds := s.cfg.ImageGeneration.RecoveryIntervalSeconds
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// ==================== Leader Lock ====================

// tryAcquireLeaderLock 尝试获取 leader lock。
// simple 模式直接放行；集群模式优先 Redis SetNX，失败降级 DB advisory lock。
func (s *ImageGenerationDispatcher) tryAcquireLeaderLock(ctx context.Context) (func(), bool) {
	if s == nil {
		return nil, false
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil, true
	}
	key := imageGenDispatcherLeaderLockKeyDefault
	ttl := imageGenDispatcherLeaderLockTTLDefault

	if s.redis != nil {
		ok, err := s.redis.SetNX(ctx, key, s.instanceID, ttl).Result()
		if err == nil {
			if !ok {
				return nil, false
			}
			return func() {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, _ = imageGenDispatcherReleaseScript.Run(releaseCtx, s.redis, []string{key}, s.instanceID).Result()
			}, true
		}
		slog.Warn("image generation dispatcher: redis leader lock failed, falling back to DB advisory lock",
			"error", err)
	}
	if s.db != nil {
		release, ok := tryAcquireDBAdvisoryLock(ctx, s.db, hashAdvisoryLockID(key))
		return release, ok
	}
	return nil, false
}

// ==================== Heartbeat ====================

func (s *ImageGenerationDispatcher) recordHeartbeatSuccess(startedAt time.Time, duration time.Duration, result DispatcherRunResult) {
	if s.opsRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageGenDispatcherHeartbeatTimeout)
	defer cancel()
	runAt := startedAt
	now := startedAt
	durMs := duration.Milliseconds()
	lastResult := fmt.Sprintf(`{"dispatched":%d,"recovered_stale":%d,"duration_ms":%d}`,
		result.Dispatched, result.RecoveredStale, result.DurationMs)
	if err := s.opsRepo.UpsertJobHeartbeat(ctx, &OpsUpsertJobHeartbeatInput{
		JobName:        imageGenDispatcherJobName,
		LastRunAt:      &runAt,
		LastSuccessAt:  &now,
		LastDurationMs: &durMs,
		LastResult:     &lastResult,
	}); err != nil {
		slog.Warn("image generation dispatcher: failed to record heartbeat", "error", err)
	}
}

func (s *ImageGenerationDispatcher) recordHeartbeatError(startedAt time.Time, duration time.Duration, err error) {
	if s.opsRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageGenDispatcherHeartbeatTimeout)
	defer cancel()
	runAt := startedAt
	now := startedAt
	durMs := duration.Milliseconds()
	errMsg := err.Error()
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	lastResult := "error: " + errMsg
	if err := s.opsRepo.UpsertJobHeartbeat(ctx, &OpsUpsertJobHeartbeatInput{
		JobName:        imageGenDispatcherJobName,
		LastRunAt:      &runAt,
		LastErrorAt:    &now,
		LastError:      &errMsg,
		LastDurationMs: &durMs,
		LastResult:     &lastResult,
	}); err != nil {
		slog.Warn("image generation dispatcher: failed to record heartbeat error", "error", err)
	}
}
