package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ImageCredentialService 管理员侧的 Agnes 凭据管理服务。
//
// 安全约束：
//   - API Key 加密存储（AES-256-GCM）
//   - key_fingerprint 仅展示末尾四位
//   - 不返回加密后的密文和明文
//   - 测试接口只返回脱敏信息
type ImageCredentialService struct {
	repo      ImageCredentialRepository
	encryptor SecretEncryptor
	agnes     AgnesClient
	cfg       CredentialSchedulerConfig
}

// NewImageCredentialService 构造凭据管理服务。
func NewImageCredentialService(
	repo ImageCredentialRepository,
	encryptor SecretEncryptor,
	agnes AgnesClient,
	cfg CredentialSchedulerConfig,
) *ImageCredentialService {
	return &ImageCredentialService{
		repo:      repo,
		encryptor: encryptor,
		agnes:     agnes,
		cfg:       cfg,
	}
}

// CredentialDTO 是返回给管理后台的凭据数据（脱敏）。
// 不包含加密密文和明文 API Key。
type CredentialDTO struct {
	ID                  int64      `json:"id"`
	Name                string     `json:"name"`
	Provider            string     `json:"provider"`
	KeyFingerprint      string     `json:"key_fingerprint"`
	Enabled             bool       `json:"enabled"`
	Priority            int        `json:"priority"`
	Weight              int        `json:"weight"`
	Status              string     `json:"status"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastUsedAt          *time.Time `json:"last_used_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastFailureAt       *time.Time `json:"last_failure_at"`
	CooldownUntil       *time.Time `json:"cooldown_until"`
	LastErrorCode       *string    `json:"last_error_code"`
	LastErrorMessage    *string    `json:"last_error_message"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// CreateCredentialRequest 创建凭据请求。
type CreateCredentialRequest struct {
	Name     string `json:"name"`
	Provider string `json:"provider"` // 默认 agnes
	APIKey   string `json:"api_key"`  // 明文，加密后存储
	Enabled  bool   `json:"enabled"`
	// Priority 使用指针类型以区分"未指定"（nil → 用默认 50）和"0"（合法优先级 0）。
	Priority *int   `json:"priority"`
	Weight   *int   `json:"weight"`
}

// UpdateCredentialRequest 更新凭据请求。
// APIKey 为空表示不更新密钥。
type UpdateCredentialRequest struct {
	Name     string `json:"name"`
	APIKey   string `json:"api_key"` // 空表示不更新
	Enabled  *bool  `json:"enabled"`
	Priority *int   `json:"priority"`
	Weight   *int   `json:"weight"`
}

// TestCredentialResult 测试凭据连接的结果。
//
// 字段说明：
//   - Success: 结构化成功标记。前端必须基于此字段（而非 HTTP 200）判断测试是否通过
//   - ErrorCode: 兼容字段，保留历史大写格式（DECRYPT_FAILED/AUTH_FAILED/FORBIDDEN/...）
//   - Reason: 标准化错误原因（小写下划线格式），新增字段，前端应优先使用
//     取值：success / decrypt_failed / auth_failed / forbidden / rate_limited / upstream_error / timeout
//   - HTTPStatus: 上游返回的 HTTP 状态码（解密失败时为 0）
//
// 解密失败与上游认证失败的区分：
//   - decrypt_failed: 本地 AES 解密失败（密钥不匹配/密文损坏），不调用上游
//   - auth_failed:    上游返回 401
//   - forbidden:      上游返回 403
type TestCredentialResult struct {
	Success      bool   `json:"success"`
	HTTPStatus   int    `json:"http_status"`
	DurationMs   int    `json:"duration_ms"`
	ErrorCode    string `json:"error_code,omitempty"`
	Reason       string `json:"reason,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	Fingerprint  string `json:"key_fingerprint"`
}

// 凭据测试结果的原因常量（小写下划线格式，用于 TestCredentialResult.Reason）。
const (
	TestCredentialReasonSuccess       = "success"
	TestCredentialReasonDecryptFailed = "decrypt_failed"
	TestCredentialReasonAuthFailed    = "auth_failed"
	TestCredentialReasonForbidden     = "forbidden"
	TestCredentialReasonRateLimited   = "rate_limited"
	TestCredentialReasonUpstreamError = "upstream_error"
	TestCredentialReasonTimeout       = "timeout"
)

// ListCredentials 列出所有凭据（管理员视图）。
func (s *ImageCredentialService) ListCredentials(ctx context.Context) ([]*CredentialDTO, error) {
	creds, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*CredentialDTO, 0, len(creds))
	for _, c := range creds {
		out = append(out, toCredentialDTO(c))
	}
	return out, nil
}

// GetCredential 获取单个凭据（管理员视图）。
func (s *ImageCredentialService) GetCredential(ctx context.Context, id int64) (*CredentialDTO, error) {
	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toCredentialDTO(c), nil
}

// CreateCredential 创建凭据（加密存储 API Key）。
func (s *ImageCredentialService) CreateCredential(ctx context.Context, req CreateCredentialRequest) (*CredentialDTO, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errImageInvalidRequest("credential name is required")
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		return nil, errImageInvalidRequest("api_key is required")
	}
	provider := req.Provider
	if provider == "" {
		provider = PlatformAgnes
	}

	// 加密 API Key
	encrypted, err := s.encryptor.Encrypt(apiKey)
	if err != nil {
		return nil, errImageStorageFailed()
	}

	// 生成指纹（末尾四位）
	fingerprint := keyFingerprint(apiKey)

	// 默认值：Priority/Weight 为 nil 时使用默认值，为 0 时保留 0
	enabled := req.Enabled
	priority := 50 // 默认优先级
	if req.Priority != nil {
		priority = *req.Priority
	}
	weight := 1 // 默认权重
	if req.Weight != nil {
		weight = *req.Weight
	}

	cred, err := s.repo.Create(ctx, &ImageProviderCredential{
		Name:            name,
		Provider:        provider,
		ApiKeyEncrypted: encrypted,
		KeyFingerprint:  fingerprint,
		Enabled:         enabled,
		Priority:        priority,
		Weight:          weight,
		Status:          ImageCredentialStatusHealthy,
	})
	if err != nil {
		return nil, err
	}
	return toCredentialDTO(cred), nil
}

// UpdateCredential 更新凭据。
func (s *ImageCredentialService) UpdateCredential(ctx context.Context, id int64, req UpdateCredentialRequest) (*CredentialDTO, error) {
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// 更新名称
	if req.Name != "" {
		existing.Name = strings.TrimSpace(req.Name)
	}

	// 更新 API Key（如果提供了新的）
	if strings.TrimSpace(req.APIKey) != "" {
		encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(req.APIKey))
		if err != nil {
			return nil, errImageStorageFailed()
		}
		existing.ApiKeyEncrypted = encrypted
		existing.KeyFingerprint = keyFingerprint(req.APIKey)
	}

	// 更新启用状态
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
		if !existing.Enabled {
			existing.Status = ImageCredentialStatusDisabled
		} else if existing.Status == ImageCredentialStatusDisabled {
			existing.Status = ImageCredentialStatusHealthy
		}
	}

	// 更新优先级和权重
	if req.Priority != nil {
		existing.Priority = *req.Priority
	}
	if req.Weight != nil {
		existing.Weight = *req.Weight
	}

	cred, err := s.repo.Update(ctx, existing)
	if err != nil {
		return nil, err
	}
	return toCredentialDTO(cred), nil
}

// DeleteCredential 删除凭据。
// 注意：已关联的历史生成记录中的 provider_credential_id 不会级联删除（保留历史引用）。
func (s *ImageCredentialService) DeleteCredential(ctx context.Context, id int64) error {
	return s.repo.Delete(ctx, id)
}

// TestCredential 测试凭据是否可用。
// 采用"探测模式"：发送一个无效 model 的请求，利用 Agnes 先鉴权后处理的特点，
// 快速判断 key 有效性（无需等待真实生图，通常 < 2s，不消耗用户配额）。
//
// 判定逻辑：
//   - 解密失败 → decrypt_failed（本地问题，不调用上游）
//   - 401 → auth_failed（上游认证失败）
//   - 403 → forbidden（上游授权拒绝）
//   - 429 → rate_limited（上游限流，但 key 本身有效）
//   - 超时 → timeout（key 通过鉴权但响应超时）
//   - 其他错误（含 503 model_not_found、5xx）→ upstream_error（key 有效但上游有问题）
//   - 200 → success（不应该发生，但以防万一当成功处理）
//
// 前端必须检查 result.Success 字段（不能仅凭 HTTP 200 判断）。
// 解密失败时不会调用 AgnesClient，避免无意义的网络请求。
func (s *ImageCredentialService) TestCredential(ctx context.Context, id int64) (*TestCredentialResult, error) {
	cred, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// 解密 Key
	plain, err := s.encryptor.Decrypt(cred.ApiKeyEncrypted)
	if err != nil {
		// 解密失败：不调用上游，直接返回失败结果
		// 日志不记录密文/密钥；error_message 仅描述类别，不含敏感信息
		return &TestCredentialResult{
			Success:      false,
			ErrorCode:    "DECRYPT_FAILED",
			Reason:       TestCredentialReasonDecryptFailed,
			ErrorMessage: "failed to decrypt api key",
			Fingerprint:  cred.KeyFingerprint,
		}, nil
	}

	// 探测请求：使用不存在的 model，触发 Agnes 的 model_not_found 错误。
	// Agnes 的鉴权在请求处理前完成：
	//   - key 无效 → 401（快速返回，通常 < 1s）
	//   - key 有效 → 503 model_not_found（快速返回，不生成图片）
	start := time.Now()
	testReq := AgnesGenerateRequest{
		Model:  "__credential_probe__", // 故意无效的 model，触发快速错误响应
		Prompt: "test",
		Size:   "1K",
	}
	opts := AgnesCallOptions{
		DialTimeout:           s.cfg.DialTimeout,
		ResponseHeaderTimeout: s.cfg.ResponseHeaderTimeout,
		TotalTimeout:          10 * time.Second, // 探测超时，通常 2s 内返回
	}

	_, httpStatus, callErr := s.agnes.Generate(ctx, plain, testReq, opts)
	durationMs := int(time.Since(start).Milliseconds())

	if callErr != nil {
		// 401 → auth_failed
		if httpStatus == 401 {
			return &TestCredentialResult{
				Success:      false,
				HTTPStatus:   httpStatus,
				DurationMs:   durationMs,
				ErrorCode:    "AUTH_FAILED",
				Reason:       TestCredentialReasonAuthFailed,
				ErrorMessage: "API key is invalid or unauthorized",
				Fingerprint:  cred.KeyFingerprint,
			}, nil
		}
		// 403 → forbidden
		if httpStatus == 403 {
			return &TestCredentialResult{
				Success:      false,
				HTTPStatus:   httpStatus,
				DurationMs:   durationMs,
				ErrorCode:    "FORBIDDEN",
				Reason:       TestCredentialReasonForbidden,
				ErrorMessage: "API key is forbidden",
				Fingerprint:  cred.KeyFingerprint,
			}, nil
		}
		// 429 → rate_limited（key 有效但被限流）
		if httpStatus == 429 {
			return &TestCredentialResult{
				Success:      true,
				HTTPStatus:   httpStatus,
				DurationMs:   durationMs,
				ErrorCode:    upstreamErrCode(httpStatus, callErr),
				Reason:       TestCredentialReasonRateLimited,
				ErrorMessage: sanitizeUpstreamErrorMessage(callErr.Error()),
				Fingerprint:  cred.KeyFingerprint,
			}, nil
		}
		// 超时 → timeout（key 通过鉴权但响应超时）
		if isTimeoutErr(callErr) {
			return &TestCredentialResult{
				Success:      true,
				HTTPStatus:   httpStatus,
				DurationMs:   durationMs,
				ErrorCode:    upstreamErrCode(httpStatus, callErr),
				Reason:       TestCredentialReasonTimeout,
				ErrorMessage: sanitizeUpstreamErrorMessage(callErr.Error()),
				Fingerprint:  cred.KeyFingerprint,
			}, nil
		}
		// 其他错误（model_not_found 的 503、5xx、网络错误）→ key 有效但上游有问题
		// 因为 Agnes 先鉴权后处理，只要不是 401/403 就说明 key 通过了鉴权
		return &TestCredentialResult{
			Success:      true,
			HTTPStatus:   httpStatus,
			DurationMs:   durationMs,
			ErrorCode:    upstreamErrCode(httpStatus, callErr),
			Reason:       TestCredentialReasonUpstreamError,
			ErrorMessage: sanitizeUpstreamErrorMessage(callErr.Error()),
			Fingerprint:  cred.KeyFingerprint,
		}, nil
	}

	// 不应该到达这里（无效 model 不会返回 200），但以防万一当成成功
	return &TestCredentialResult{
		Success:     true,
		HTTPStatus:  httpStatus,
		DurationMs:  durationMs,
		Reason:      TestCredentialReasonSuccess,
		Fingerprint: cred.KeyFingerprint,
	}, nil
}

// ==================== 辅助函数 ====================

// toCredentialDTO 把 service 层模型转为管理后台 DTO（脱敏）。
func toCredentialDTO(c *ImageProviderCredential) *CredentialDTO {
	return &CredentialDTO{
		ID:                  c.ID,
		Name:                c.Name,
		Provider:            c.Provider,
		KeyFingerprint:      c.KeyFingerprint,
		Enabled:             c.Enabled,
		Priority:            c.Priority,
		Weight:              c.Weight,
		Status:              c.Status,
		ConsecutiveFailures: c.ConsecutiveFailures,
		LastUsedAt:          c.LastUsedAt,
		LastSuccessAt:       c.LastSuccessAt,
		LastFailureAt:       c.LastFailureAt,
		CooldownUntil:       c.CooldownUntil,
		LastErrorCode:       c.LastErrorCode,
		LastErrorMessage:    c.LastErrorMessage,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
	}
}

// keyFingerprint 生成 API Key 的指纹（仅末尾四位）。
// 用于后台识别，不泄露完整密钥。
func keyFingerprint(apiKey string) string {
	if len(apiKey) <= 4 {
		return "****"
	}
	return "****" + apiKey[len(apiKey)-4:]
}

// 编译期断言
var _ = fmt.Sprintf
