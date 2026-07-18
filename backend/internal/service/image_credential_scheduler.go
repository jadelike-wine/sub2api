package service

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ImageProviderCredential 是 image_provider_credentials 表的 service 层映射。
// ApiKeyEncrypted 是加密后的密文；解密后的明文由 Scheduler 内部持有，不返回给调用方。
type ImageProviderCredential struct {
	ID                  int64
	Name                string
	Provider            string
	ApiKeyEncrypted     string
	ApiKeyPlain         string `json:"-"` // 仅 scheduler 内部使用，绝不序列化
	KeyFingerprint      string
	Enabled             bool
	Priority            int
	Weight              int
	Status              string // healthy | unhealthy | disabled
	ConsecutiveFailures int
	LastUsedAt          *time.Time
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	CooldownUntil       *time.Time
	LastErrorCode       *string
	LastErrorMessage    *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ImageCredentialRepository 是凭据表的 repository 接口（service 层声明，repository 层实现）。
type ImageCredentialRepository interface {
	// ListSchedulable 返回所有 enabled 且未过冷却期的凭据（按 priority 升序）。
	ListSchedulable(ctx context.Context, provider string) ([]*ImageProviderCredential, error)
	// ListAll 返回所有凭据（含禁用/冷却中），供管理后台。
	ListAll(ctx context.Context) ([]*ImageProviderCredential, error)
	GetByID(ctx context.Context, id int64) (*ImageProviderCredential, error)
	Create(ctx context.Context, c *ImageProviderCredential) (*ImageProviderCredential, error)
	Update(ctx context.Context, c *ImageProviderCredential) (*ImageProviderCredential, error)
	Delete(ctx context.Context, id int64) error
	// UpdateHealth 原子更新健康状态字段（失败计数、冷却、最近成功/失败时间）。
	UpdateHealth(ctx context.Context, id int64, update CredentialHealthUpdate) error
}

// CredentialHealthUpdate 描述一次健康状态更新。
type CredentialHealthUpdate struct {
	Status              *string
	ConsecutiveFailures *int
	LastUsedAt          *time.Time
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	CooldownUntil       *time.Time
	LastErrorCode       *string
	LastErrorMessage    *string
}

// CredentialScheduler 选择一个健康的 Agnes API Key 并处理故障转移。
// 不要把选择逻辑散落在 Handler 中。
//
// 接口分两层：
//   - 兼容层：SelectAndGenerate（同步选 + 调 + failover，单次任务用完即走）
//   - 排队层：HasAvailableCredential / TryAcquireCredential / GenerateWithCredential
//     用于"Token 忙碌自动排队"场景：先 TryAcquireCredential 原子占用一个 Token，
//     若占用失败则任务进入 queued 队列；释放由 release() 显式触发，确保 Token 不会被同一任务重复占用。
type CredentialScheduler interface {
	// SelectAndGenerate 选择一个凭据调用 Agnes 生图，失败时按错误分类决定切换 Key 或直接返回。
	// 返回最终使用的凭据 ID、上游结果和错误（错误已脱敏）。
	SelectAndGenerate(ctx context.Context, req AgnesGenerateRequest) (credentialID int64, result *AgnesGenerateResult, err error)

	// HasAvailableCredential 检查是否存在空闲（未被占用）且健康的凭据。
	// 仅做查询，不占用，供 dispatcher 判断是否需要扫描 queued 队列。
	HasAvailableCredential(ctx context.Context) bool

	// TryAcquireCredential 原子地占用一个空闲凭据（Redis SETNX + 内存降级）。
	// 返回 (credentialID, release, true) 表示成功占用；release() 必须在调用方使用完毕后调用以释放占用。
	// 返回 (0, nil, false) 表示所有凭据都被占用或无可用凭据（调用方应将任务放入 queued 队列）。
	// release() 是幂等的，多次调用安全。
	TryAcquireCredential(ctx context.Context) (credentialID int64, release func(), ok bool)

	// GenerateWithCredential 使用已占用的凭据调用 Agnes 生图。
	// credentialID 必须通过 TryAcquireCredential 获得。
	// 失败时返回 ImageGenError（已分类，调用方决定是否换 Key 重试）。
	GenerateWithCredential(ctx context.Context, credentialID int64, req AgnesGenerateRequest) (result *AgnesGenerateResult, err error)
}

// CredentialSchedulerConfig 调度器配置。
type CredentialSchedulerConfig struct {
	Provider              string
	MaxAttempts           int
	Cooldown429Seconds    int
	Cooldown429MaxSeconds int
	CooldownAuthSeconds   int
	Cooldown5xxSeconds    int
	DialTimeout           time.Duration
	ResponseHeaderTimeout time.Duration
	TotalTimeout          time.Duration
}

// classifyUpstreamError 把上游错误分类为是否可切换 Key 重试。
// 返回 retryable=true 表示可切换到下一把 Key 重试（401/403/429/5xx/网络/超时）；
// retryable=false 表示是参数/内容策略错误，不应换 Key（直接返回业务错误）。
func classifyUpstreamError(httpStatus int, err error) (retryable bool, imageErr *ImageGenError) {
	// 网络错误 / 超时 → 可切换
	if err != nil && httpStatus == 0 {
		// 区分超时与其他网络错误
		if isTimeoutErr(err) {
			return true, errImageProviderTimeout()
		}
		return true, errImageProviderError(err.Error())
	}
	switch {
	case httpStatus == http.StatusUnauthorized || httpStatus == http.StatusForbidden:
		// 认证失败：可切换 Key（可能是该 Key 失效），但冷却时间更长
		return true, errImageProviderAuthFailed()
	case httpStatus == http.StatusTooManyRequests:
		// 限流：可切换 Key
		return true, errImageProviderRateLimited()
	case httpStatus >= 500:
		// 上游临时 5xx：可切换 Key
		return true, errImageProviderError(err.Error())
	case httpStatus == http.StatusBadRequest:
		// 400：参数错误 / 内容策略，不应切换 Key
		// err 里的 message 已脱敏
		msg := "invalid request"
		if err != nil {
			msg = err.Error()
		}
		return false, errImageInvalidRequest(sanitizeUpstreamErrorMessage(msg))
	case httpStatus >= 400 && httpStatus < 500:
		// 其他 4xx：保守起见不切换（如 404/413/415）
		msg := "upstream rejected request"
		if err != nil {
			msg = err.Error()
		}
		return false, errImageProviderError(sanitizeUpstreamErrorMessage(msg))
	default:
		return false, errImageProviderError(err.Error())
	}
}

// isTimeoutErr 判断是否为超时错误。
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	// context.DeadlineExceeded / net.Error.Timeout()
	type timeoutErr interface{ Timeout() bool }
	var ne timeoutErr
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return errors.Is(err, context.DeadlineExceeded)
}
