package service

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// agnesCredentialScheduler 是 CredentialScheduler 的默认实现。
//
// 调度策略：
//  1. 从 repo.ListSchedulable 取候选（enabled && 未过冷却），按 priority 升序、id 升序
//  2. Redis 原子游标做全局 round-robin（多实例一致）；Redis 不可用降级内存游标（记警告）
//  3. failover loop：按错误分类决定是否切换下一把 Key，最多 MaxAttempts 次
//  4. 同一把 Key 在同一次请求中最多尝试一次
//
// 注意：Weight 字段当前不参与调度，仅作为元数据展示。实际行为是优先级分组内的纯 round-robin。
//
// 用户隔离：所有用户共享同一 Key 池，用户不能指定 Key。
//
// Token 占用与释放（排队层）：
//   - 占用：Redis SETNX（key=agnes:image:cred:lock:{id}, value=token, TTL=TotalTimeout+缓冲）
//     降级：Redis 不可用时使用内存 sync.Map，仅单实例有效
//   - 释放：Lua 脚本仅持有者可 DEL，幂等（多次调用安全）
//   - TTL 兜底：即使释放失败，TTL 到期后自动解锁，避免 Token 永久占用
type agnesCredentialScheduler struct {
	repo      ImageCredentialRepository
	agnes     AgnesClient
	encryptor SecretEncryptor
	rdb       *redis.Client
	cfg       CredentialSchedulerConfig

	// 内存降级游标（Redis 不可用时用）
	memCursor atomic.Int64

	// 内存降级锁池（Redis 不可用时用），key=credentialID(int64), value=token(string)
	memLocks sync.Map
}

// imageCredReleaseScript 仅持有者可释放，与 imageAssetCleanupReleaseScript 一致模式。
var imageCredReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// imageCredLockTTL 返回 Token 占用锁的 TTL。
// 取生成超时 + 30s 缓冲，确保整个生图流程（含下载/上传）都能覆盖；
// cfg.TotalTimeout <= 0 时退化为 5 分钟。
func (s *agnesCredentialScheduler) imageCredLockTTL() time.Duration {
	ttl := s.cfg.TotalTimeout
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return ttl + 30*time.Second
}

// imageCredLockKey 构造 Redis 占用锁 key。
func imageCredLockKey(credentialID int64) string {
	return "agnes:image:cred:lock:" + strconv.FormatInt(credentialID, 10)
}

// NewAgnesCredentialScheduler 构造调度器。rdb 为 nil 时降级到内存游标。
func NewAgnesCredentialScheduler(
	repo ImageCredentialRepository,
	agnes AgnesClient,
	encryptor SecretEncryptor,
	rdb *redis.Client,
	cfg CredentialSchedulerConfig,
) CredentialScheduler {
	return &agnesCredentialScheduler{
		repo:      repo,
		agnes:     agnes,
		encryptor: encryptor,
		rdb:       rdb,
		cfg:       cfg,
	}
}

// SelectAndGenerate 选择凭据并调用 Agnes 生图，失败按分类切换 Key。
func (s *agnesCredentialScheduler) SelectAndGenerate(ctx context.Context, req AgnesGenerateRequest) (int64, *AgnesGenerateResult, error) {
	candidates, err := s.repo.ListSchedulable(ctx, s.cfg.Provider)
	if err != nil {
		return 0, nil, errImageProviderError("failed to load credentials: " + err.Error()).WithCause(err)
	}
	if len(candidates) == 0 {
		return 0, nil, errImageNoAvailableCredential()
	}

	// 解密所有候选的 API Key（仅在此处临时持有明文，不返回、不记录日志）
	for _, c := range candidates {
		plain, derr := s.encryptor.Decrypt(c.ApiKeyEncrypted)
		if derr != nil {
			slog.Warn("image credential decrypt failed, skipping", "credential_id", c.ID, "error", derr)
			c.ApiKeyPlain = ""
			continue
		}
		c.ApiKeyPlain = plain
	}

	// 全局 round-robin 起点
	startIdx := s.nextIndex(ctx, len(candidates))

	maxAttempts := s.cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if maxAttempts > len(candidates) {
		maxAttempts = len(candidates)
	}

	opts := AgnesCallOptions{
		DialTimeout:           s.cfg.DialTimeout,
		ResponseHeaderTimeout: s.cfg.ResponseHeaderTimeout,
		TotalTimeout:          s.cfg.TotalTimeout,
	}

	tried := make(map[int64]bool, maxAttempts)
	var lastErr *ImageGenError

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// 从起点轮询找下一个未尝试且可用的候选
		idx := -1
		for offset := 0; offset < len(candidates); offset++ {
			i := (startIdx + offset) % len(candidates)
			c := candidates[i]
			if tried[c.ID] || c.ApiKeyPlain == "" {
				continue
			}
			idx = i
			break
		}
		if idx == -1 {
			break // 所有候选都已尝试
		}
		cred := candidates[idx]
		tried[cred.ID] = true

		now := time.Now()
		// 标记最近使用时间（best-effort，失败不阻塞）
		_ = s.repo.UpdateHealth(ctx, cred.ID, CredentialHealthUpdate{LastUsedAt: &now})

		result, httpStatus, callErr := s.agnes.Generate(ctx, cred.ApiKeyPlain, req, opts)

		if callErr == nil && result != nil {
			// 成功：清零失败计数，更新最近成功时间，恢复健康状态
			healthy := "healthy"
			zero := 0
			_ = s.repo.UpdateHealth(ctx, cred.ID, CredentialHealthUpdate{
				Status:              &healthy,
				ConsecutiveFailures: &zero,
				LastSuccessAt:       &now,
			})
			return cred.ID, result, nil
		}

		// 失败：分类
		retryable, imageErr := classifyUpstreamError(httpStatus, callErr)
		// 记录失败到凭据（best-effort）
		s.recordFailure(ctx, cred, httpStatus, callErr, retryable)

		lastErr = imageErr
		if !retryable {
			// 参数/内容策略错误：不切换 Key，直接返回
			return cred.ID, nil, imageErr
		}
		// 可重试：切换下一把 Key
	}

	if lastErr == nil {
		lastErr = errImageNoAvailableCredential()
	}
	return 0, nil, lastErr
}

// ==================== 排队层：Token 占用/释放 ====================

// HasAvailableCredential 检查是否存在空闲（未被占用）且健康的凭据。
// 仅查询不占用，供 dispatcher 判断是否需要扫描 queued 队列。
// Redis 不可用时降级为内存检查（仅当前实例可见的占用）。
func (s *agnesCredentialScheduler) HasAvailableCredential(ctx context.Context) bool {
	candidates, err := s.repo.ListSchedulable(ctx, s.cfg.Provider)
	if err != nil || len(candidates) == 0 {
		return false
	}
	for _, c := range candidates {
		if !s.isCredentialLocked(ctx, c.ID) {
			return true
		}
	}
	return false
}

// TryAcquireCredential 原子地占用一个空闲凭据。
// 使用 Redis SETNX 在多实例间互斥；Redis 不可用降级为内存 sync.Map（仅单实例有效）。
// 返回 (credentialID, release, true) 表示占用成功；release() 必须在调用方使用完毕后调用。
// 返回 (0, nil, false) 表示所有凭据都被占用或无可用凭据。
// release() 幂等，多次调用安全。
func (s *agnesCredentialScheduler) TryAcquireCredential(ctx context.Context) (int64, func(), bool) {
	candidates, err := s.repo.ListSchedulable(ctx, s.cfg.Provider)
	if err != nil {
		slog.Warn("agnes scheduler: TryAcquireCredential failed to load candidates", "error", err)
		return 0, nil, false
	}
	if len(candidates) == 0 {
		return 0, nil, false
	}

	// 使用 round-robin 起点遍历候选，避免总是从第一个开始
	startIdx := s.nextIndex(ctx, len(candidates))
	ttl := s.imageCredLockTTL()

	for offset := 0; offset < len(candidates); offset++ {
		idx := (startIdx + offset) % len(candidates)
		cred := candidates[idx]

		token := uuid.NewString()
		if ok := s.acquireLock(ctx, cred.ID, token, ttl); !ok {
			continue
		}

		// 占用成功：构造幂等 release
		var releaseOnce sync.Once
		release := func() {
			releaseOnce.Do(func() {
				s.releaseLock(ctx, cred.ID, token)
			})
		}
		return cred.ID, release, true
	}
	return 0, nil, false
}

// GenerateWithCredential 使用已占用的凭据调用 Agnes 生图。
// credentialID 必须通过 TryAcquireCredential 获得（调用方负责 release）。
// 失败时返回 ImageGenError，调用方决定是否换 Key 重试。
// 本方法不负责 release，调用方在使用完后必须显式调用 release()。
func (s *agnesCredentialScheduler) GenerateWithCredential(ctx context.Context, credentialID int64, req AgnesGenerateRequest) (*AgnesGenerateResult, error) {
	cred, err := s.repo.GetByID(ctx, credentialID)
	if err != nil {
		return nil, errImageProviderError("failed to load credential: " + err.Error()).WithCause(err)
	}
	if !cred.Enabled {
		return nil, errImageProviderAuthFailed()
	}

	plain, derr := s.encryptor.Decrypt(cred.ApiKeyEncrypted)
	if derr != nil {
		slog.Warn("image credential decrypt failed in GenerateWithCredential",
			"credential_id", credentialID, "error", derr)
		return nil, errImageProviderAuthFailed().WithCause(derr)
	}

	opts := AgnesCallOptions{
		DialTimeout:           s.cfg.DialTimeout,
		ResponseHeaderTimeout: s.cfg.ResponseHeaderTimeout,
		TotalTimeout:          s.cfg.TotalTimeout,
	}

	now := time.Now()
	_ = s.repo.UpdateHealth(ctx, cred.ID, CredentialHealthUpdate{LastUsedAt: &now})

	result, httpStatus, callErr := s.agnes.Generate(ctx, plain, req, opts)
	if callErr == nil && result != nil {
		healthy := "healthy"
		zero := 0
		_ = s.repo.UpdateHealth(ctx, cred.ID, CredentialHealthUpdate{
			Status:              &healthy,
			ConsecutiveFailures: &zero,
			LastSuccessAt:       &now,
		})
		return result, nil
	}

	// 失败：分类 + 记录到凭据健康状态
	retryable, imageErr := classifyUpstreamError(httpStatus, callErr)
	s.recordFailure(ctx, cred, httpStatus, callErr, retryable)
	return nil, imageErr
}

// acquireLock 占用凭据锁。Redis 优先，不可用降级到内存 sync.Map。
// 返回 true 表示占用成功；false 表示锁已被持有或占用失败。
func (s *agnesCredentialScheduler) acquireLock(ctx context.Context, credentialID int64, token string, ttl time.Duration) bool {
	key := imageCredLockKey(credentialID)
	if s.rdb != nil {
		ok, err := s.rdb.SetNX(ctx, key, token, ttl).Result()
		if err == nil {
			return ok
		}
		slog.Warn("agnes scheduler: redis SetNX failed, falling back to in-memory lock",
			"credential_id", credentialID, "error", err)
	}
	// 内存降级：LoadOrStore 原子操作
	_, loaded := s.memLocks.LoadOrStore(credentialID, token)
	if loaded {
		return false
	}
	// 启动兜底清理 goroutine，避免内存锁永久持有
	go func() {
		timer := time.NewTimer(ttl)
		defer timer.Stop()
		<-timer.C
		s.memLocks.CompareAndDelete(credentialID, token)
	}()
	return true
}

// releaseLock 释放凭据锁。Redis 使用 Lua 脚本仅持有者可释放；内存使用 CAS 删除。
// 幂等：锁已不存在或 token 不匹配时静默返回。
func (s *agnesCredentialScheduler) releaseLock(ctx context.Context, credentialID int64, token string) {
	key := imageCredLockKey(credentialID)
	if s.rdb != nil {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = imageCredReleaseScript.Run(releaseCtx, s.rdb, []string{key}, token).Result()
		return
	}
	s.memLocks.CompareAndDelete(credentialID, token)
}

// isCredentialLocked 检查凭据是否被占用（不占用）。
// Redis：检查 key 是否存在；内存：检查 sync.Map。
func (s *agnesCredentialScheduler) isCredentialLocked(ctx context.Context, credentialID int64) bool {
	key := imageCredLockKey(credentialID)
	if s.rdb != nil {
		n, err := s.rdb.Exists(ctx, key).Result()
		if err == nil {
			return n > 0
		}
		// Redis 出错时降级检查内存（保守认为 Redis 不可用）
	}
	_, loaded := s.memLocks.Load(credentialID)
	return loaded
}

// recordFailure 把失败记录到凭据（更新冷却、失败计数）。
func (s *agnesCredentialScheduler) recordFailure(ctx context.Context, cred *ImageProviderCredential, httpStatus int, callErr error, retryable bool) {
	now := time.Now()
	failures := cred.ConsecutiveFailures + 1
	errCode := upstreamErrCode(httpStatus, callErr)
	errMsg := ""
	if callErr != nil {
		errMsg = sanitizeUpstreamErrorMessage(callErr.Error())
	}

	cooldown := s.computeCooldown(httpStatus, failures)
	var cooldownUntil *time.Time
	if cooldown > 0 {
		until := now.Add(cooldown)
		cooldownUntil = &until
	}

	status := cred.Status
	if httpStatus == 401 || httpStatus == 403 {
		status = "unhealthy"
	}

	_ = s.repo.UpdateHealth(ctx, cred.ID, CredentialHealthUpdate{
		Status:              &status,
		ConsecutiveFailures: &failures,
		LastFailureAt:       &now,
		CooldownUntil:       cooldownUntil,
		LastErrorCode:       &errCode,
		LastErrorMessage:    &errMsg,
	})
}

// computeCooldown 根据错误类型和连续失败次数计算冷却时间（指数退避）。
func (s *agnesCredentialScheduler) computeCooldown(httpStatus int, consecutiveFailures int) time.Duration {
	var base int
	switch {
	case httpStatus == 429:
		base = s.cfg.Cooldown429Seconds
	case httpStatus == 401 || httpStatus == 403:
		base = s.cfg.CooldownAuthSeconds
	case httpStatus >= 500:
		base = s.cfg.Cooldown5xxSeconds
	default:
		// 网络错误：短期冷却
		base = s.cfg.Cooldown5xxSeconds
	}
	if base <= 0 {
		base = 60
	}
	// 指数退避：base * 2^(failures-1)，上限封顶
	max := s.cfg.Cooldown429MaxSeconds
	if max <= 0 {
		max = 1800
	}
	backoff := base
	for i := 1; i < consecutiveFailures; i++ {
		backoff *= 2
		if backoff >= max {
			backoff = max
			break
		}
	}
	return time.Duration(backoff) * time.Second
}

// nextIndex 全局 round-robin 游标。
// Redis 优先（原子 INCR），不可用降级到内存游标（记警告）。
func (s *agnesCredentialScheduler) nextIndex(ctx context.Context, count int) int {
	if count <= 0 {
		return 0
	}
	key := "agnes:image:key:round_robin"
	if s.rdb != nil {
		val, err := s.rdb.Incr(ctx, key).Result()
		if err == nil {
			// 防止游标无限增长，定期重置
			if val > 1<<31 {
				_ = s.rdb.Set(ctx, key, 0, 0).Err()
			}
			return int((val - 1) % int64(count))
		}
		// Redis 不可用：降级到内存游标，记警告
		slog.Warn("agnes scheduler redis round_robin unavailable, falling back to in-memory cursor", "error", err)
	}
	// 内存降级游标（单实例 round-robin，多实例下可能不严格全局，但有降级总比固定第一把好）
	next := int(s.memCursor.Add(1) - 1)
	return next % count
}

func upstreamErrCode(httpStatus int, err error) string {
	if httpStatus > 0 {
		switch httpStatus {
		case 429:
			return "RATE_LIMITED"
		case 401:
			return "AUTH_FAILED"
		case 403:
			return "FORBIDDEN"
		case 400:
			return "INVALID_REQUEST"
		}
		if httpStatus >= 500 {
			return "UPSTREAM_5XX"
		}
	}
	if err != nil {
		if isTimeoutErr(err) {
			return "TIMEOUT"
		}
		return "NETWORK_ERROR"
	}
	return "UNKNOWN"
}

// 编译期断言：确保 scheduler 实现接口
var _ CredentialScheduler = (*agnesCredentialScheduler)(nil)
