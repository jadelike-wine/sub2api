//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// =====================================================================
// 测试目标：
//   1. 用户未达到并发上限时可以创建任务
//   2. 达到并发上限时返回 409 (IMAGE_CONCURRENT_LIMIT)
//   3. 多个并发请求不会绕过限制（advisory lock 串行化）—— 见 repository 集成测试
//   4. 所有 Token 忙碌时任务进入 queued
//   5. Token 释放后，排队任务自动变为 processing
//   6. 服务重启后，queued 任务仍能继续被调度
//   7. Token 调用失败后不会永久占用
// =====================================================================

// testImageB64 是一个 1x1 透明 PNG 的 Base64 编码。
// 测试中使用 B64JSON 模式而非 URL 模式，避免发起真实 HTTP 下载请求。
const testImageB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// ----- 测试用 mock 实现 -----

// fakeImageGenRepo 模拟 ImageGenerationRepository，记录所有调用。
type fakeImageGenRepo struct {
	mu sync.Mutex

	// CreateIfUnderUserConcurrency 行为控制
	createFn func(ctx context.Context, params CreateImageGenerationParams, maxConcurrent int) (*ImageGeneration, error)

	// 状态记录
	createCalls       int
	createCallMaxConc []int
	claimed           map[int64]bool // taskID -> 当前是否处于 claimed 状态
	everClaimed       map[int64]bool // taskID -> 曾经被 claim 过（即使后续 revert 也保留 true）
	reverted          []int64
	queuedList        []*ImageGeneration
	claimedStatus     map[int64]string // taskID -> final status
	statusUpdates     map[int64]UpdateImageGenerationStatusParams
	staleList         []*ImageGeneration
	staleFilter       StaleProcessingFilter
	countActiveResult int
	countActiveErr    error
}

func newFakeImageGenRepo() *fakeImageGenRepo {
	return &fakeImageGenRepo{
		claimed:       make(map[int64]bool),
		everClaimed:   make(map[int64]bool),
		claimedStatus: make(map[int64]string),
		statusUpdates: make(map[int64]UpdateImageGenerationStatusParams),
	}
}

func (r *fakeImageGenRepo) Create(ctx context.Context, params CreateImageGenerationParams) (*ImageGeneration, error) {
	return r.CreateIfUnderUserConcurrency(ctx, params, 0)
}

func (r *fakeImageGenRepo) CreateIfUnderUserConcurrency(ctx context.Context, params CreateImageGenerationParams, maxConcurrent int) (*ImageGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	r.createCallMaxConc = append(r.createCallMaxConc, maxConcurrent)
	if r.createFn != nil {
		return r.createFn(ctx, params, maxConcurrent)
	}
	// 默认：成功创建一个 queued 任务
	gen := &ImageGeneration{
		ID:             int64(r.createCalls),
		UserID:         params.UserID,
		ConversationID: params.ConversationID,
		Provider:       params.Provider,
		Model:          params.Model,
		GenerationType: params.GenerationType,
		Prompt:         params.Prompt,
		Size:           params.Size,
		Ratio:          params.Ratio,
		Status:         params.Status,
		IdempotencyKey: params.IdempotencyKey,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	r.claimedStatus[gen.ID] = params.Status
	return gen, nil
}

func (r *fakeImageGenRepo) GetByIDForOwner(ctx context.Context, userID, id int64) (*ImageGeneration, error) {
	return nil, ErrImageGenerationNotFound
}

func (r *fakeImageGenRepo) GetByID(ctx context.Context, id int64) (*ImageGeneration, error) {
	return nil, ErrImageGenerationNotFound
}

func (r *fakeImageGenRepo) GetByIdempotencyKey(ctx context.Context, userID int64, key string) (*ImageGeneration, error) {
	return nil, ErrImageGenerationNotFound
}

func (r *fakeImageGenRepo) List(ctx context.Context, filter ImageGenerationFilter) (*ImageGenerationList, error) {
	return &ImageGenerationList{}, nil
}

func (r *fakeImageGenRepo) ListByConversation(ctx context.Context, userID, conversationID int64) ([]*ImageGeneration, error) {
	return nil, nil
}

func (r *fakeImageGenRepo) UpdateStatus(ctx context.Context, userID, id int64, params UpdateImageGenerationStatusParams) (*ImageGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statusUpdates[id] = params
	if params.Status != nil {
		r.claimedStatus[id] = *params.Status
	}
	return &ImageGeneration{ID: id, UserID: userID, Status: derefStrImg(params.Status)}, nil
}

func (r *fakeImageGenRepo) ListStaleProcessing(ctx context.Context, filter StaleProcessingFilter) ([]*ImageGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staleFilter = filter
	return r.staleList, nil
}

func (r *fakeImageGenRepo) ListQueued(ctx context.Context, limit int) ([]*ImageGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queuedList, nil
}

func (r *fakeImageGenRepo) ClaimQueued(ctx context.Context, taskID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed[taskID] {
		return false, nil
	}
	r.claimed[taskID] = true
	r.everClaimed[taskID] = true
	r.claimedStatus[taskID] = ImageStatusProcessing
	return true, nil
}

func (r *fakeImageGenRepo) RevertToQueued(ctx context.Context, taskID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reverted = append(r.reverted, taskID)
	r.claimed[taskID] = false
	r.claimedStatus[taskID] = ImageStatusQueued
	return nil
}

func (r *fakeImageGenRepo) CountActiveByUser(ctx context.Context, userID int64) (int, error) {
	return r.countActiveResult, r.countActiveErr
}

func (r *fakeImageGenRepo) SoftDelete(ctx context.Context, userID, id int64) error { return nil }

func derefStrImg(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// fakeConvRepo 模拟会话 repository。
type fakeConvRepo struct {
	created *ImageConversation
}

func (r *fakeConvRepo) Create(ctx context.Context, params CreateImageConversationParams) (*ImageConversation, error) {
	r.created = &ImageConversation{ID: 1, UserID: params.UserID, Title: params.Title}
	return r.created, nil
}
func (r *fakeConvRepo) GetByIDForOwner(ctx context.Context, userID, id int64) (*ImageConversation, error) {
	if r.created != nil && r.created.ID == id {
		return r.created, nil
	}
	return nil, ErrImageConversationNotFound
}
func (r *fakeConvRepo) List(ctx context.Context, filter ImageConversationFilter) (*ImageConversationList, error) {
	return &ImageConversationList{}, nil
}
func (r *fakeConvRepo) Update(ctx context.Context, userID, id int64, params UpdateImageConversationParams) (*ImageConversation, error) {
	return r.created, nil
}
func (r *fakeConvRepo) TouchLastMessageAt(ctx context.Context, userID, id int64, at time.Time) error {
	return nil
}
func (r *fakeConvRepo) SoftDelete(ctx context.Context, userID, id int64) error { return nil }

// fakeAssetRepo 模拟资产 repository。
type fakeAssetRepo struct{}

func (r *fakeAssetRepo) Create(ctx context.Context, params CreateImageAssetParams) (*ImageAsset, error) {
	return &ImageAsset{ID: 1}, nil
}
func (r *fakeAssetRepo) GetByIDForOwner(ctx context.Context, userID, id int64) (*ImageAsset, error) {
	return nil, ErrImageAssetNotFound
}
func (r *fakeAssetRepo) GetByS3KeyForOwner(ctx context.Context, userID int64, s3Key string) (*ImageAsset, error) {
	return nil, ErrImageAssetNotFound
}
func (r *fakeAssetRepo) List(ctx context.Context, filter ImageAssetFilter) ([]*ImageAsset, error) {
	return nil, nil
}
func (r *fakeAssetRepo) ListByGeneration(ctx context.Context, userID, generationID int64) ([]*ImageAsset, error) {
	return nil, nil
}
func (r *fakeAssetRepo) LinkAssetsToGeneration(ctx context.Context, userID, generationID int64, assetIDs []int64) error {
	return nil
}
func (r *fakeAssetRepo) SoftDelete(ctx context.Context, userID, id int64) error { return nil }
func (r *fakeAssetRepo) SoftDeleteByGeneration(ctx context.Context, userID, generationID int64) error {
	return nil
}
func (r *fakeAssetRepo) ListSoftDeletedBefore(ctx context.Context, cutoff time.Time, limit int) ([]*ImageAsset, error) {
	return nil, nil
}
func (r *fakeAssetRepo) CountSoftDeletedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return 0, nil
}
func (r *fakeAssetRepo) HardDelete(ctx context.Context, id int64) error { return nil }

// fakeCredRepo 模拟凭据 repository。
type fakeCredRepo struct{}

func (r *fakeCredRepo) ListSchedulable(ctx context.Context, provider string) ([]*ImageProviderCredential, error) {
	return nil, nil
}
func (r *fakeCredRepo) ListAll(ctx context.Context) ([]*ImageProviderCredential, error) {
	return nil, nil
}
func (r *fakeCredRepo) GetByID(ctx context.Context, id int64) (*ImageProviderCredential, error) {
	return nil, ErrImageCredentialNotFound
}
func (r *fakeCredRepo) Create(ctx context.Context, c *ImageProviderCredential) (*ImageProviderCredential, error) {
	return c, nil
}
func (r *fakeCredRepo) Update(ctx context.Context, c *ImageProviderCredential) (*ImageProviderCredential, error) {
	return c, nil
}
func (r *fakeCredRepo) Delete(ctx context.Context, id int64) error { return nil }
func (r *fakeCredRepo) UpdateHealth(ctx context.Context, id int64, update CredentialHealthUpdate) error {
	return nil
}

// fakeScheduler 模拟 CredentialScheduler，可控制 TryAcquireCredential 和 GenerateWithCredential 行为。
type fakeScheduler struct {
	mu sync.Mutex

	// TryAcquireCredential 行为：返回的 ok 决定是否占用成功
	acquireOk bool
	// acquireCount 记录 TryAcquireCredential 被调用次数
	acquireCount int32

	// releaseCount 记录 release() 被调用次数（统计 Token 是否被释放）
	releaseCount int32

	// GenerateWithCredential 行为
	genResult *AgnesGenerateResult
	genErr    error
	genCalls  int32

	// hasAvailable 控制 HasAvailableCredential 返回值
	hasAvailable bool
}

func (s *fakeScheduler) SelectAndGenerate(ctx context.Context, req AgnesGenerateRequest) (int64, *AgnesGenerateResult, error) {
	return 0, nil, errImageNoAvailableCredential()
}

func (s *fakeScheduler) HasAvailableCredential(ctx context.Context) bool {
	return s.hasAvailable
}

func (s *fakeScheduler) TryAcquireCredential(ctx context.Context) (int64, func(), bool) {
	atomic.AddInt32(&s.acquireCount, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.acquireOk {
		return 0, nil, false
	}
	// 模拟占用一个 Token，返回 release 函数
	var once sync.Once
	release := func() {
		once.Do(func() {
			atomic.AddInt32(&s.releaseCount, 1)
		})
	}
	return 100, release, true
}

func (s *fakeScheduler) GenerateWithCredential(ctx context.Context, credentialID int64, req AgnesGenerateRequest) (*AgnesGenerateResult, error) {
	atomic.AddInt32(&s.genCalls, 1)
	return s.genResult, s.genErr
}

// fakeStorage 模拟 EnovaImageAssetStorage。
type fakeStorage struct{ configured bool }

func (s *fakeStorage) Put(ctx context.Context, input PutObjectInput) (*StoredObject, error) {
	return &StoredObject{Bucket: "test", Key: input.Key, Size: 100, MimeType: input.ContentType}, nil
}
func (s *fakeStorage) Delete(ctx context.Context, key string) error { return nil }
func (s *fakeStorage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	return "https://example.com/" + key, nil
}
func (s *fakeStorage) PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, error) {
	return "https://example.com/put/" + key, nil
}
func (s *fakeStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStorage) Head(ctx context.Context, key string) (*ObjectHead, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStorage) Bucket() string   { return "test" }
func (s *fakeStorage) Configured() bool { return s.configured }
func (s *fakeStorage) Driver() string   { return "s3" }

// 为简化测试，构造一个完整的 service 实例。
// 复用 user_service_test.go 中的 mockUserRepo（实现完整 UserRepository 接口）。
func newTestImageGenService(t *testing.T, cfg config.ImageGenerationConfig) (
	*ImageGenerationService,
	*fakeImageGenRepo,
	*fakeScheduler,
	*fakeConvRepo,
) {
	t.Helper()
	genRepo := newFakeImageGenRepo()
	convRepo := &fakeConvRepo{}
	assetRepo := &fakeAssetRepo{}
	credRepo := &fakeCredRepo{}
	sched := &fakeScheduler{}
	storage := &fakeStorage{configured: true}
	userRepo := &mockUserRepo{getByIDUser: &User{ID: 1, Balance: 1000.0}}

	svc := &ImageGenerationService{
		conversationRepo: convRepo,
		generationRepo:   genRepo,
		assetRepo:        assetRepo,
		credentialRepo:   credRepo,
		scheduler:        sched,
		storage:          storage,
		usageRepo:        nil, // 测试不记录 usage
		userRepo:         userRepo,
		settingService:   nil, // 测试不读 settings
		cfg:              cfg,
	}
	return svc, genRepo, sched, convRepo
}

func defaultTestImageGenConfig() config.ImageGenerationConfig {
	return config.ImageGenerationConfig{
		Enabled:                     true,
		AgnesModel:                  "test-model",
		MaxConcurrentPerUser:        3,
		MaxAttemptsPerGeneration:    2,
		MaxInputImagesPerGen:        6,
		MaxPromptChars:              1000,
		Price2KUSD:                  0.1,
		StaleProcessingAfterSeconds: 1,
	}
}

// =====================================================================
// 场景 1：用户未达到并发上限时可以创建任务
// =====================================================================

func TestImageGenConcurrent_CreateSucceeds_UnderLimit(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// 配置 scheduler：有可用 Token，调用成功
	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genResult = &AgnesGenerateResult{B64JSON: testImageB64, MimeType: "image/png"}

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "a cat",
		Size:   "2K",
		Ratio:  "1:1",
	}

	gen, err := svc.CreateGeneration(context.Background(), 1, req)
	require.NoError(t, err)
	require.NotNil(t, gen)
	require.Equal(t, ImageStatusQueued, gen.Status)

	// 验证 CreateIfUnderUserConcurrency 被调用，并传入了正确的 maxConcurrent
	require.Equal(t, 1, genRepo.createCalls)
	require.Equal(t, []int{3}, genRepo.createCallMaxConc)

	// 给异步 tryDispatch 一点时间执行
	time.Sleep(100 * time.Millisecond)

	// 验证 Token 被占用并调用上游
	require.GreaterOrEqual(t, atomic.LoadInt32(&sched.acquireCount), int32(1))
	require.GreaterOrEqual(t, atomic.LoadInt32(&sched.genCalls), int32(1))
}

// =====================================================================
// 场景 2：达到并发上限时返回 409 (IMAGE_CONCURRENT_LIMIT)
// =====================================================================

func TestImageGenConcurrent_CreateFails_AtLimit_Returns409(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	svc, genRepo, _, _ := newTestImageGenService(t, cfg)

	// 让 Repository 在 CreateIfUnderUserConcurrency 时返回 ErrImageConcurrentLimitReached
	genRepo.createFn = func(ctx context.Context, params CreateImageGenerationParams, maxConcurrent int) (*ImageGeneration, error) {
		return nil, ErrImageConcurrentLimitReached
	}

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "a dog",
		Size:   "2K",
		Ratio:  "1:1",
	}

	gen, err := svc.CreateGeneration(context.Background(), 1, req)
	require.Error(t, err)
	require.Nil(t, gen)

	// 验证错误是 IMAGE_CONCURRENT_LIMIT，HTTP 409
	var appErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, int32(409), appErr.Code)
	require.Equal(t, "IMAGE_CONCURRENT_LIMIT", appErr.Reason)

	// 验证 Repository 被调用了一次（不是 0 次）
	require.Equal(t, 1, genRepo.createCalls)
}

// =====================================================================
// 场景 4：所有 Token 忙碌时任务进入 queued（保持 queued 状态）
// =====================================================================

func TestImageGenConcurrent_AllTokensBusy_StaysQueued(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// 配置 scheduler：所有 Token 都被占用，TryAcquireCredential 返回 false
	sched.acquireOk = false
	sched.hasAvailable = false

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "a bird",
		Size:   "2K",
		Ratio:  "1:1",
	}

	gen, err := svc.CreateGeneration(context.Background(), 1, req)
	require.NoError(t, err)
	require.Equal(t, ImageStatusQueued, gen.Status)

	// 等待异步 tryDispatch 执行：应调用 ClaimQueued 成功，但 TryAcquireCredential 失败，
	// 然后调用 RevertToQueued 回退为 queued
	time.Sleep(150 * time.Millisecond)

	genRepo.mu.Lock()
	defer genRepo.mu.Unlock()
	// 任务被 Claim 过（processing），然后被 Revert 回 queued
	require.True(t, genRepo.everClaimed[gen.ID], "task should be claimed first")
	require.Contains(t, genRepo.reverted, gen.ID, "task should be reverted to queued")
	require.Equal(t, ImageStatusQueued, genRepo.claimedStatus[gen.ID])
}

// =====================================================================
// 场景 5：Token 释放后，排队任务自动变为 processing（通过 DispatchQueuedBatch）
// =====================================================================

func TestImageGenConcurrent_DispatchQueuedBatch_TokenFreed_ClaimsAndProcesses(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// 先把一个 queued 任务塞进 Repository
	queuedTask := &ImageGeneration{
		ID:             42,
		UserID:         1,
		ConversationID: 1,
		Provider:       PlatformAgnes,
		Model:          cfg.AgnesModel,
		GenerationType: ImageGenerationTypeTextToImage,
		Prompt:         "a fish",
		Size:           "2K",
		Ratio:          "1:1",
		Status:         ImageStatusQueued,
		CreatedAt:      time.Now(),
	}
	genRepo.queuedList = []*ImageGeneration{queuedTask}

	// 配置 scheduler：有可用 Token，调用成功
	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genResult = &AgnesGenerateResult{B64JSON: testImageB64, MimeType: "image/png"}

	// DispatchQueuedBatch 应该触发调度
	dispatched, err := svc.DispatchQueuedBatch(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, dispatched, "should dispatch one task")

	// 等待异步 tryDispatch 执行
	time.Sleep(150 * time.Millisecond)

	// 验证任务被 Claim（曾经进入 processing），最终变为 succeeded
	genRepo.mu.Lock()
	defer genRepo.mu.Unlock()
	require.True(t, genRepo.everClaimed[42], "queued task should be claimed")
	require.Equal(t, ImageStatusSucceeded, genRepo.claimedStatus[42],
		"queued task should be processed to succeeded after token freed")

	// 验证 Token 被占用和调用
	require.GreaterOrEqual(t, atomic.LoadInt32(&sched.acquireCount), int32(1))
	require.GreaterOrEqual(t, atomic.LoadInt32(&sched.genCalls), int32(1))
}

// =====================================================================
// 场景 6：服务重启后，queued 任务仍能继续被调度
//         （通过 RecoverStaleGenerations + DispatchQueuedBatch）
// =====================================================================

func TestImageGenConcurrent_RestartRecoversQueuedTasks(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	cfg.StaleProcessingAfterSeconds = 1 // 1 秒后视为卡死
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// 模拟服务重启后数据库中有：
	//   1) 一个卡死 processing 任务（started_at 早已过期）
	//   2) 一个 queued 任务等待调度
	pastTime := time.Now().Add(-10 * time.Second)
	staleTask := &ImageGeneration{
		ID:             100,
		UserID:         1,
		ConversationID: 1,
		Provider:       PlatformAgnes,
		Model:          cfg.AgnesModel,
		GenerationType: ImageGenerationTypeTextToImage,
		Prompt:         "stale",
		Size:           "2K",
		Ratio:          "1:1",
		Status:         ImageStatusProcessing,
		StartedAt:      &pastTime,
		CreatedAt:      pastTime,
	}
	queuedTask := &ImageGeneration{
		ID:             101,
		UserID:         1,
		ConversationID: 1,
		Provider:       PlatformAgnes,
		Model:          cfg.AgnesModel,
		GenerationType: ImageGenerationTypeTextToImage,
		Prompt:         "queued",
		Size:           "2K",
		Ratio:          "1:1",
		Status:         ImageStatusQueued,
		CreatedAt:      time.Now(),
	}
	genRepo.staleList = []*ImageGeneration{staleTask}
	genRepo.queuedList = []*ImageGeneration{queuedTask}

	// 配置 scheduler：Token 可用
	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genResult = &AgnesGenerateResult{B64JSON: testImageB64, MimeType: "image/png"}

	ctx := context.Background()

	// 1. RecoverStaleGenerations：将卡死任务标记为 failed
	recovered, err := svc.RecoverStaleGenerations(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	// 验证卡死任务被标记为 failed
	genRepo.mu.Lock()
	upd := genRepo.statusUpdates[100]
	genRepo.mu.Unlock()
	require.NotNil(t, upd.Status)
	require.Equal(t, ImageStatusFailed, *upd.Status)
	require.NotNil(t, upd.ErrorCode)
	require.Equal(t, "STALE_TIMEOUT", *upd.ErrorCode)

	// 2. DispatchQueuedBatch：调度 queued 任务
	dispatched, err := svc.DispatchQueuedBatch(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, dispatched)

	// 等待异步 tryDispatch 执行
	time.Sleep(150 * time.Millisecond)

	genRepo.mu.Lock()
	defer genRepo.mu.Unlock()
	require.True(t, genRepo.claimed[101], "queued task should be claimed after restart recovery")
}

// =====================================================================
// 场景 7：Token 调用失败后不会永久占用（release 被调用，可换下一个 Token）
// =====================================================================

func TestImageGenConcurrent_TokenCallFailure_ReleasesAndRetries(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	cfg.MaxAttemptsPerGeneration = 3 // 允许 3 次尝试
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// 配置 scheduler：占用成功，但调用上游始终失败（可重试错误，例如 5xx）
	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genErr = errImageProviderError("upstream 500")

	// 直接调用 tryDispatch（绕过 CreateGeneration 的复杂流程）
	// 先把任务标记为 queued 并 Claim
	taskID := int64(200)
	genRepo.claimedStatus[taskID] = ImageStatusQueued

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "fail test",
		Size:   "2K",
		Ratio:  "1:1",
	}

	// 手动触发 tryDispatch
	svc.tryDispatch(taskID, 1, 1, nil, req)

	// 验证：所有尝试都失败后，任务被标记为 failed
	genRepo.mu.Lock()
	defer genRepo.mu.Unlock()
	finalStatus := genRepo.claimedStatus[taskID]
	require.Equal(t, ImageStatusFailed, finalStatus, "task should be marked failed after all retries")

	// 关键断言：release 被调用过（Token 没有被永久占用）
	// MaxAttemptsPerGeneration=3，每次失败都会调用 release
	require.GreaterOrEqual(t, atomic.LoadInt32(&sched.releaseCount), int32(1),
		"release must be called after failure to avoid permanent Token occupation")

	// 验证 ClaimQueued 被调用过（任务进入 processing）
	require.True(t, genRepo.claimed[taskID], "task should have been claimed first")

	// 验证错误码是 UPSTREAM_FAILED 或对应 reason
	upd, ok := genRepo.statusUpdates[taskID]
	require.True(t, ok, "should have status update for failed task")
	require.NotNil(t, upd.ErrorCode)
}

// =====================================================================
// 场景 7 补充：Token 调用成功后 release 被调用（避免泄漏）
// =====================================================================

func TestImageGenConcurrent_TokenCallSuccess_ReleasesAfterCompletion(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	cfg.MaxAttemptsPerGeneration = 2
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// 配置 scheduler：占用成功，调用成功
	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genResult = &AgnesGenerateResult{
		B64JSON:  testImageB64,
		MimeType: "image/png",
	}

	taskID := int64(300)
	genRepo.claimedStatus[taskID] = ImageStatusQueued

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "success test",
		Size:   "2K",
		Ratio:  "1:1",
	}

	svc.tryDispatch(taskID, 1, 1, nil, req)

	// 验证：任务最终标记为 succeeded
	genRepo.mu.Lock()
	defer genRepo.mu.Unlock()
	require.Equal(t, ImageStatusSucceeded, genRepo.claimedStatus[taskID])

	// 验证 release 被调用（Token 被释放，可服务下一个任务）
	require.GreaterOrEqual(t, atomic.LoadInt32(&sched.releaseCount), int32(1),
		"release must be called after success to free Token")
}

// =====================================================================
// 场景 2 补充：并发上限为 0 时不启用并发检查（退化为普通 Create）
// =====================================================================

func TestImageGenConcurrent_ZeroLimit_DisablesCheck(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	cfg.MaxConcurrentPerUser = 0 // 关闭并发检查
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genResult = &AgnesGenerateResult{B64JSON: testImageB64, MimeType: "image/png"}

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "no limit",
		Size:   "2K",
		Ratio:  "1:1",
	}

	gen, err := svc.CreateGeneration(context.Background(), 1, req)
	require.NoError(t, err)
	require.NotNil(t, gen)

	// 验证 maxConcurrent=0 被传入（Repository 会退化为普通 Create）
	require.Equal(t, []int{0}, genRepo.createCallMaxConc)
}

// =====================================================================
// 场景：不可重试错误（IMAGE_INVALID_REQUEST）不换 Token 直接 markFailed
// =====================================================================

func TestImageGenConcurrent_NonRetryableError_MarksFailedImmediately(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	cfg.MaxAttemptsPerGeneration = 5
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// 配置 scheduler：占用成功，但返回不可重试错误（400 参数错误）
	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genErr = errImageInvalidRequest("bad prompt")

	taskID := int64(400)
	genRepo.claimedStatus[taskID] = ImageStatusQueued

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "bad",
		Size:   "2K",
		Ratio:  "1:1",
	}

	svc.tryDispatch(taskID, 1, 1, nil, req)

	// 验证：只调用了 1 次 TryAcquireCredential（不可重试不换 Key）
	require.Equal(t, int32(1), atomic.LoadInt32(&sched.acquireCount),
		"non-retryable error should not switch to next Token")

	// 验证：release 被调用（Token 已释放）
	require.GreaterOrEqual(t, atomic.LoadInt32(&sched.releaseCount), int32(1))

	// 验证：任务被标记为 failed
	genRepo.mu.Lock()
	defer genRepo.mu.Unlock()
	require.Equal(t, ImageStatusFailed, genRepo.claimedStatus[taskID])

	upd, ok := genRepo.statusUpdates[taskID]
	require.True(t, ok)
	require.NotNil(t, upd.ErrorCode)
	require.Equal(t, "IMAGE_INVALID_REQUEST", *upd.ErrorCode)
}
