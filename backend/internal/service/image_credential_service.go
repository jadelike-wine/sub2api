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
type TestCredentialResult struct {
	Success      bool   `json:"success"`
	HTTPStatus   int    `json:"http_status"`
	DurationMs   int    `json:"duration_ms"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	Fingerprint  string `json:"key_fingerprint"`
}

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
//   - 401/403 → key 无效（失败）
//   - 其他状态码（含 503 model_not_found、5xx、超时）→ key 有效（通过鉴权）
//   - 200 → key 有效（不应该发生，但以防万一当成功处理）
func (s *ImageCredentialService) TestCredential(ctx context.Context, id int64) (*TestCredentialResult, error) {
	cred, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// 解密 Key
	plain, err := s.encryptor.Decrypt(cred.ApiKeyEncrypted)
	if err != nil {
		return &TestCredentialResult{
			Success:      false,
			ErrorCode:    "DECRYPT_FAILED",
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
		// 401/403 → key 无效
		if httpStatus == 401 || httpStatus == 403 {
			return &TestCredentialResult{
				Success:      false,
				HTTPStatus:   httpStatus,
				DurationMs:   durationMs,
				ErrorCode:    "INVALID_KEY",
				ErrorMessage: "API key is invalid or unauthorized",
				Fingerprint:  cred.KeyFingerprint,
			}, nil
		}
		// 其他错误（model_not_found 的 503、超时、5xx）→ key 有效但上游有问题
		// 因为 Agnes 先鉴权后处理，只要不是 401/403 就说明 key 通过了鉴权
		return &TestCredentialResult{
			Success:      true,
			HTTPStatus:   httpStatus,
			DurationMs:   durationMs,
			ErrorCode:    upstreamErrCode(httpStatus, callErr),
			ErrorMessage: sanitizeUpstreamErrorMessage(callErr.Error()),
			Fingerprint:  cred.KeyFingerprint,
		}, nil
	}

	// 不应该到达这里（无效 model 不会返回 200），但以防万一当成成功
	return &TestCredentialResult{
		Success:     true,
		HTTPStatus:  httpStatus,
		DurationMs:  durationMs,
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
