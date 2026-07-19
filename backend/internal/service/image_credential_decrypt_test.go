//go:build unit

package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// localAESEncryptor 是与 repository.AESEncryptor 实现完全一致的本地版本。
// 复制实现是为了避免 service 包循环引用 repository 包。
// 格式：base64(nonce + ciphertext + tag)，使用 AES-256-GCM。
type localAESEncryptor struct {
	key []byte
}

func newLocalAESEncryptorFromHex(keyHex string) (*localAESEncryptor, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("key must be 32 bytes")
	}
	return &localAESEncryptor{key: key}, nil
}

func (e *localAESEncryptor) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (e *localAESEncryptor) Decrypt(ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// =====================================================================
// 测试目标：
//   1. 密钥不匹配时 AES 解密失败返回 IMAGE_CREDENTIAL_DECRYPT_FAILED
//   2. 解密失败时 AgnesClient 调用次数为 0
//   3. 单个凭据解密失败时可以尝试其他有效凭据
//   4. 所有候选凭据解密失败时保留 IMAGE_CREDENTIAL_DECRYPT_FAILED（不退化为 NO_AVAILABLE_CREDENTIAL/PROVIDER_AUTH_FAILED）
//   5. 真正的上游 401/403 仍返回 IMAGE_PROVIDER_AUTH_FAILED
//   6. GenerateWithCredential 解密失败时不调用 AgnesClient，不更新凭据为 unhealthy
//   7. 管理后台 TestCredential 接口正确返回 decrypt_failed / auth_failed / forbidden / rate_limited / timeout / upstream_error / success
//   8. 使用同一固定密钥重启后，历史密文仍能正常解密
// =====================================================================

// ---- mock 实现 ----

// fakeEncryptor 可控制 Encrypt/Decrypt 行为，用于模拟解密成功/失败。
type fakeEncryptor struct {
	// decryptErr 非空时 Decrypt 返回该错误
	decryptErr error
	// decryptResult Decrypt 成功时返回的明文
	decryptResult string
	// decryptCalls 记录 Decrypt 被调用次数
	decryptCalls int32
}

func (e *fakeEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (e *fakeEncryptor) Decrypt(ciphertext string) (string, error) {
	atomic.AddInt32(&e.decryptCalls, 1)
	if e.decryptErr != nil {
		return "", e.decryptErr
	}
	if e.decryptResult != "" {
		return e.decryptResult, nil
	}
	// 默认返回 ciphertext 去掉 "enc:" 前缀作为明文
	if strings.HasPrefix(ciphertext, "enc:") {
		return strings.TrimPrefix(ciphertext, "enc:"), nil
	}
	return ciphertext, nil
}

// fakeAgnesClient 可控制 Generate 的返回值，统计调用次数。
type fakeAgnesClient struct {
	// generateFn 自定义行为
	generateFn func(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error)
	// calls 记录 Generate 被调用次数
	calls int32
	// lastAPIKey 记录最近一次调用传入的 apiKey（验证解密后的明文是否被正确传递）
	lastAPIKey string
}

func (c *fakeAgnesClient) Generate(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
	atomic.AddInt32(&c.calls, 1)
	c.lastAPIKey = apiKey
	if c.generateFn != nil {
		return c.generateFn(ctx, apiKey, req, opts)
	}
	// 默认返回成功
	return &AgnesGenerateResult{B64JSON: testImageB64, MimeType: "image/png"}, http.StatusOK, nil
}

// fakeCredRepoForScheduler 用于 scheduler 测试的凭据 repo mock。
type fakeCredRepoForScheduler struct {
	// list 返回的候选凭据
	schedulable []*ImageProviderCredential
	// byID 按 ID 查询的凭据
	byID map[int64]*ImageProviderCredential
	// updateHealthCalls 记录 UpdateHealth 被调用次数
	updateHealthCalls int32
	// lastHealthUpdate 最近一次 UpdateHealth 参数
	lastHealthUpdate CredentialHealthUpdate
	// lastHealthUpdateID 最近一次 UpdateHealth 的凭据 ID
	lastHealthUpdateID int64
}

func (r *fakeCredRepoForScheduler) ListSchedulable(ctx context.Context, provider string) ([]*ImageProviderCredential, error) {
	return r.schedulable, nil
}

func (r *fakeCredRepoForScheduler) ListAll(ctx context.Context) ([]*ImageProviderCredential, error) {
	return r.schedulable, nil
}

func (r *fakeCredRepoForScheduler) GetByID(ctx context.Context, id int64) (*ImageProviderCredential, error) {
	if c, ok := r.byID[id]; ok {
		return c, nil
	}
	return nil, ErrImageCredentialNotFound
}

func (r *fakeCredRepoForScheduler) Create(ctx context.Context, c *ImageProviderCredential) (*ImageProviderCredential, error) {
	return c, nil
}

func (r *fakeCredRepoForScheduler) Update(ctx context.Context, c *ImageProviderCredential) (*ImageProviderCredential, error) {
	return c, nil
}

func (r *fakeCredRepoForScheduler) Delete(ctx context.Context, id int64) error { return nil }

func (r *fakeCredRepoForScheduler) UpdateHealth(ctx context.Context, id int64, update CredentialHealthUpdate) error {
	atomic.AddInt32(&r.updateHealthCalls, 1)
	r.lastHealthUpdateID = id
	r.lastHealthUpdate = update
	return nil
}

// makeTestCred 构造测试凭据。
func makeTestCred(id int64, encrypted string) *ImageProviderCredential {
	return &ImageProviderCredential{
		ID:              id,
		Name:            "test-cred",
		Provider:        "agnes",
		ApiKeyEncrypted: encrypted,
		KeyFingerprint:  "****abcd",
		Enabled:         true,
		Priority:        50,
		Status:          "healthy",
	}
}

// makeTestSchedulerConfig 构造测试用调度器配置。
func makeTestSchedulerConfig() CredentialSchedulerConfig {
	return CredentialSchedulerConfig{
		Provider:              "agnes",
		MaxAttempts:           3,
		Cooldown429Seconds:    60,
		Cooldown429MaxSeconds: 1800,
		CooldownAuthSeconds:   300,
		Cooldown5xxSeconds:    60,
		DialTimeout:           5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		TotalTimeout:          30 * time.Second,
	}
}

// =====================================================================
// 场景 1：GenerateWithCredential 解密失败时返回 IMAGE_CREDENTIAL_DECRYPT_FAILED
// =====================================================================

func TestGenerateWithCredential_DecryptFailed_ReturnsDecryptFailedCode(t *testing.T) {
	encryptor := &fakeEncryptor{
		decryptErr: errors.New("decrypt: cipher: message authentication failed"),
	}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:encrypted-key-1"),
		},
	}
	agnes := &fakeAgnesClient{}
	scheduler := NewAgnesCredentialScheduler(repo, agnes, encryptor, nil, makeTestSchedulerConfig())

	_, err := scheduler.GenerateWithCredential(context.Background(), 1, AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	require.Error(t, err)
	appErr, ok := err.(*ImageGenError)
	require.True(t, ok, "err must be *ImageGenError")
	require.Equal(t, "IMAGE_CREDENTIAL_DECRYPT_FAILED", appErr.Reason,
		"decrypt failure must map to IMAGE_CREDENTIAL_DECRYPT_FAILED, not IMAGE_PROVIDER_AUTH_FAILED")
	require.NotEqual(t, "IMAGE_PROVIDER_AUTH_FAILED", appErr.Reason)
}

// =====================================================================
// 场景 2：解密失败时 AgnesClient 调用次数为 0
// =====================================================================

func TestGenerateWithCredential_DecryptFailed_AgnesClientNotCalled(t *testing.T) {
	encryptor := &fakeEncryptor{
		decryptErr: errors.New("decrypt: cipher: message authentication failed"),
	}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:encrypted-key-1"),
		},
	}
	agnes := &fakeAgnesClient{}
	scheduler := NewAgnesCredentialScheduler(repo, agnes, encryptor, nil, makeTestSchedulerConfig())

	_, _ = scheduler.GenerateWithCredential(context.Background(), 1, AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	require.Equal(t, int32(0), atomic.LoadInt32(&agnes.calls),
		"AgnesClient.Generate must not be called when decryption fails")
}

// =====================================================================
// 场景 3：解密失败时不更新凭据健康状态（不污染 unhealthy）
// =====================================================================

func TestGenerateWithCredential_DecryptFailed_DoesNotMarkUnhealthy(t *testing.T) {
	encryptor := &fakeEncryptor{
		decryptErr: errors.New("decrypt: cipher: message authentication failed"),
	}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:encrypted-key-1"),
		},
	}
	agnes := &fakeAgnesClient{}
	scheduler := NewAgnesCredentialScheduler(repo, agnes, encryptor, nil, makeTestSchedulerConfig())

	_, _ = scheduler.GenerateWithCredential(context.Background(), 1, AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	// UpdateHealth 不应被调用（解密失败不应该污染凭据健康状态）
	// 注意：UpdateHealth 在成功路径会被调用以更新 LastUsedAt，但解密失败时应早于该调用返回
	require.Equal(t, int32(0), atomic.LoadInt32(&repo.updateHealthCalls),
		"UpdateHealth must not be called when decryption fails (best-effort LastUsedAt update is also skipped)")
}

// =====================================================================
// 场景 4：单个凭据解密失败时可以尝试其他有效凭据（SelectAndGenerate）
// =====================================================================

func TestSelectAndGenerate_PartialDecryptFailure_TriesOtherCredentials(t *testing.T) {
	// 两个凭据：ID=1 解密失败，ID=2 解密成功
	// 使用 differentialEncryptor 根据 ciphertext 差异化返回 Decrypt 结果
	differentialEncryptor := &differentialEncryptor{
		failForCiphertext: "enc:bad-key",
	}

	repo := &fakeCredRepoForScheduler{
		schedulable: []*ImageProviderCredential{
			makeTestCred(1, "enc:bad-key"),      // 解密失败
			makeTestCred(2, "enc:good-key-2"),   // 解密成功
		},
	}
	agnes := &fakeAgnesClient{} // 默认返回成功
	scheduler := NewAgnesCredentialScheduler(repo, agnes, differentialEncryptor, nil, makeTestSchedulerConfig())

	credID, result, err := scheduler.SelectAndGenerate(context.Background(), AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	require.NoError(t, err, "should succeed by falling back to credential 2")
	require.NotNil(t, result)
	require.Equal(t, int64(2), credID, "should use credential 2 (credential 1 skipped due to decrypt failure)")
	// AgnesClient 应该被调用至少一次（用 credential 2 的明文 key）
	require.GreaterOrEqual(t, atomic.LoadInt32(&agnes.calls), int32(1))
}

// differentialEncryptor 根据 ciphertext 差异化返回 Decrypt 结果。
type differentialEncryptor struct {
	failForCiphertext string
}

func (e *differentialEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (e *differentialEncryptor) Decrypt(ciphertext string) (string, error) {
	if ciphertext == e.failForCiphertext {
		return "", errors.New("decrypt: cipher: message authentication failed")
	}
	if strings.HasPrefix(ciphertext, "enc:") {
		return strings.TrimPrefix(ciphertext, "enc:"), nil
	}
	return ciphertext, nil
}

// =====================================================================
// 场景 5：所有候选凭据解密失败时保留 IMAGE_CREDENTIAL_DECRYPT_FAILED
// =====================================================================

func TestSelectAndGenerate_AllDecryptFailure_ReturnsDecryptFailed(t *testing.T) {
	encryptor := &fakeEncryptor{
		decryptErr: errors.New("decrypt: cipher: message authentication failed"),
	}
	repo := &fakeCredRepoForScheduler{
		schedulable: []*ImageProviderCredential{
			makeTestCred(1, "enc:key-1"),
			makeTestCred(2, "enc:key-2"),
			makeTestCred(3, "enc:key-3"),
		},
	}
	agnes := &fakeAgnesClient{}
	scheduler := NewAgnesCredentialScheduler(repo, agnes, encryptor, nil, makeTestSchedulerConfig())

	credID, _, err := scheduler.SelectAndGenerate(context.Background(), AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	require.Error(t, err)
	require.Equal(t, int64(0), credID, "no credential should be selected")
	appErr, ok := err.(*ImageGenError)
	require.True(t, ok)
	require.Equal(t, "IMAGE_CREDENTIAL_DECRYPT_FAILED", appErr.Reason,
		"all-decrypt-failure must return IMAGE_CREDENTIAL_DECRYPT_FAILED")
	require.NotEqual(t, "IMAGE_NO_AVAILABLE_CREDENTIAL", appErr.Reason)
	require.NotEqual(t, "IMAGE_PROVIDER_AUTH_FAILED", appErr.Reason)
	require.NotEqual(t, "UPSTREAM_FAILED", appErr.Reason)
	// AgnesClient 不应被调用
	require.Equal(t, int32(0), atomic.LoadInt32(&agnes.calls))
}

// =====================================================================
// 场景 6：真正的上游 401 仍返回 IMAGE_PROVIDER_AUTH_FAILED
// =====================================================================

func TestSelectAndGenerate_Upstream401_ReturnsProviderAuthFailed(t *testing.T) {
	encryptor := &fakeEncryptor{} // 默认解密成功
	repo := &fakeCredRepoForScheduler{
		schedulable: []*ImageProviderCredential{
			makeTestCred(1, "enc:key-1"),
		},
	}
	agnes := &fakeAgnesClient{
		generateFn: func(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
			return nil, 401, errors.New("upstream 401 unauthorized")
		},
	}
	scheduler := NewAgnesCredentialScheduler(repo, agnes, encryptor, nil, makeTestSchedulerConfig())

	_, _, err := scheduler.SelectAndGenerate(context.Background(), AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	require.Error(t, err)
	appErr, ok := err.(*ImageGenError)
	require.True(t, ok)
	require.Equal(t, "IMAGE_PROVIDER_AUTH_FAILED", appErr.Reason,
		"true upstream 401 must still return IMAGE_PROVIDER_AUTH_FAILED")
	require.NotEqual(t, "IMAGE_CREDENTIAL_DECRYPT_FAILED", appErr.Reason)
}

// =====================================================================
// 场景 7：真正的上游 403 仍返回 IMAGE_PROVIDER_AUTH_FAILED（同属认证失败）
// =====================================================================

func TestSelectAndGenerate_Upstream403_ReturnsProviderAuthFailed(t *testing.T) {
	encryptor := &fakeEncryptor{}
	repo := &fakeCredRepoForScheduler{
		schedulable: []*ImageProviderCredential{
			makeTestCred(1, "enc:key-1"),
		},
	}
	agnes := &fakeAgnesClient{
		generateFn: func(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
			return nil, 403, errors.New("upstream 403 forbidden")
		},
	}
	scheduler := NewAgnesCredentialScheduler(repo, agnes, encryptor, nil, makeTestSchedulerConfig())

	_, _, err := scheduler.SelectAndGenerate(context.Background(), AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	require.Error(t, err)
	appErr, ok := err.(*ImageGenError)
	require.True(t, ok)
	require.Equal(t, "IMAGE_PROVIDER_AUTH_FAILED", appErr.Reason)
}

// =====================================================================
// 场景 8：使用同一固定密钥重启后，历史密文仍能正常解密
// 这里直接使用真实的 AES-256-GCM 实现（跨实例验证：实例 A 加密 → 实例 B 解密）
// =====================================================================

func TestAESDecrypt_SameKeyAcrossInstances_CanDecryptHistoricalCiphertext(t *testing.T) {
	// 生成固定密钥
	keyBytes := make([]byte, 32)
	_, err := rand.Read(keyBytes)
	require.NoError(t, err)
	keyHex := hex.EncodeToString(keyBytes)

	// 实例 A：使用 keyHex 加密
	encryptorA, err := newLocalAESEncryptorFromHex(keyHex)
	require.NoError(t, err)
	plaintext := "agnes-api-key-secret-12345"
	ciphertext, err := encryptorA.Encrypt(plaintext)
	require.NoError(t, err)

	// 实例 B：使用相同 keyHex 解密（模拟服务重启后用同一密钥解密历史密文）
	encryptorB, err := newLocalAESEncryptorFromHex(keyHex)
	require.NoError(t, err)
	decrypted, err := encryptorB.Decrypt(ciphertext)
	require.NoError(t, err, "decrypt should succeed with same key across instances")
	require.Equal(t, plaintext, decrypted)
}

// =====================================================================
// 场景 9：使用不同密钥（模拟重启后随机生成新密钥）解密失败
// =====================================================================

func TestAESDecrypt_DifferentKey_FailsAndMapsToDecryptFailed(t *testing.T) {
	// 密钥 A 加密
	keyA := make([]byte, 32)
	_, _ = rand.Read(keyA)
	encryptorA, err := newLocalAESEncryptorFromHex(hex.EncodeToString(keyA))
	require.NoError(t, err)
	ciphertext, err := encryptorA.Encrypt("secret-api-key")
	require.NoError(t, err)

	// 密钥 B 解密（不同密钥）
	keyB := make([]byte, 32)
	_, _ = rand.Read(keyB)
	encryptorB, err := newLocalAESEncryptorFromHex(hex.EncodeToString(keyB))
	require.NoError(t, err)
	_, err = encryptorB.Decrypt(ciphertext)
	require.Error(t, err, "decrypt with mismatched key must fail")
	// 错误消息不一定包含 "decrypt"，但必须是非 nil
}

// =====================================================================
// 场景 10：errImageCredentialDecryptFailed 的 HTTP 状态码为 503，与 NO_AVAILABLE_CREDENTIAL 一致
// 这样前端可以通过 error_code 区分但 HTTP 状态保持兼容
// =====================================================================

func TestErrImageCredentialDecryptFailed_HTTPStatusAndReason(t *testing.T) {
	err := errImageCredentialDecryptFailed()
	require.Equal(t, "IMAGE_CREDENTIAL_DECRYPT_FAILED", err.Reason)
	require.Equal(t, int32(http.StatusServiceUnavailable), err.Code,
		"HTTP status should be 503 Service Unavailable")
	require.NotEmpty(t, err.Message)
	// 确保错误消息不包含敏感信息
	require.NotContains(t, err.Message, "key")
	require.NotContains(t, err.Message, "cipher")
}

// =====================================================================
// 场景 11：管理后台 TestCredential 接口 - 解密失败返回 decrypt_failed
// =====================================================================

func TestAdminTestCredential_DecryptFailed_ReturnsDecryptFailedReason(t *testing.T) {
	encryptor := &fakeEncryptor{
		decryptErr: errors.New("decrypt: cipher: message authentication failed"),
	}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:bad-key"),
		},
	}
	agnes := &fakeAgnesClient{}
	svc := NewImageCredentialService(repo, encryptor, agnes, makeTestSchedulerConfig())

	result, err := svc.TestCredential(context.Background(), 1)
	require.NoError(t, err, "TestCredential should not return go error for decrypt failure")
	require.NotNil(t, result)
	require.False(t, result.Success, "Success must be false for decrypt failure")
	require.Equal(t, TestCredentialReasonDecryptFailed, result.Reason)
	require.Equal(t, "DECRYPT_FAILED", result.ErrorCode)
	// AgnesClient 不应被调用
	require.Equal(t, int32(0), atomic.LoadInt32(&agnes.calls))
	// Fingerprint 仍应返回（不影响识别）
	require.Equal(t, "****abcd", result.Fingerprint)
}

// =====================================================================
// 场景 12：管理后台 TestCredential 接口 - 上游 401 返回 auth_failed
// =====================================================================

func TestAdminTestCredential_Upstream401_ReturnsAuthFailedReason(t *testing.T) {
	encryptor := &fakeEncryptor{} // 解密成功
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:valid-key"),
		},
	}
	agnes := &fakeAgnesClient{
		generateFn: func(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
			return nil, 401, errors.New("upstream 401 unauthorized")
		},
	}
	svc := NewImageCredentialService(repo, encryptor, agnes, makeTestSchedulerConfig())

	result, err := svc.TestCredential(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Equal(t, TestCredentialReasonAuthFailed, result.Reason)
	require.Equal(t, "AUTH_FAILED", result.ErrorCode)
	require.Equal(t, 401, result.HTTPStatus)
}

// =====================================================================
// 场景 13：管理后台 TestCredential 接口 - 上游 403 返回 forbidden
// =====================================================================

func TestAdminTestCredential_Upstream403_ReturnsForbiddenReason(t *testing.T) {
	encryptor := &fakeEncryptor{}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:valid-key"),
		},
	}
	agnes := &fakeAgnesClient{
		generateFn: func(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
			return nil, 403, errors.New("upstream 403 forbidden")
		},
	}
	svc := NewImageCredentialService(repo, encryptor, agnes, makeTestSchedulerConfig())

	result, err := svc.TestCredential(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, TestCredentialReasonForbidden, result.Reason)
	require.Equal(t, "FORBIDDEN", result.ErrorCode)
}

// =====================================================================
// 场景 14：管理后台 TestCredential 接口 - 上游 429 返回 rate_limited
// =====================================================================

func TestAdminTestCredential_Upstream429_ReturnsRateLimitedReason(t *testing.T) {
	encryptor := &fakeEncryptor{}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:valid-key"),
		},
	}
	agnes := &fakeAgnesClient{
		generateFn: func(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
			return nil, 429, errors.New("upstream 429 too many requests")
		},
	}
	svc := NewImageCredentialService(repo, encryptor, agnes, makeTestSchedulerConfig())

	result, err := svc.TestCredential(context.Background(), 1)
	require.NoError(t, err)
	// 429 时 Success=true（key 本身有效，只是被限流）
	require.True(t, result.Success)
	require.Equal(t, TestCredentialReasonRateLimited, result.Reason)
	require.Equal(t, 429, result.HTTPStatus)
}

// =====================================================================
// 场景 15：管理后台 TestCredential 接口 - 上游 503 返回 upstream_error
// =====================================================================

func TestAdminTestCredential_Upstream503_ReturnsUpstreamErrorReason(t *testing.T) {
	encryptor := &fakeEncryptor{}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:valid-key"),
		},
	}
	agnes := &fakeAgnesClient{
		generateFn: func(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
			return nil, 503, errors.New("upstream 503 model not found")
		},
	}
	svc := NewImageCredentialService(repo, encryptor, agnes, makeTestSchedulerConfig())

	result, err := svc.TestCredential(context.Background(), 1)
	require.NoError(t, err)
	// 503 时 Success=true（key 通过鉴权，是上游其他问题）
	require.True(t, result.Success)
	require.Equal(t, TestCredentialReasonUpstreamError, result.Reason)
}

// =====================================================================
// 场景 16：管理后台 TestCredential 接口 - 超时返回 timeout
// =====================================================================

func TestAdminTestCredential_Timeout_ReturnsTimeoutReason(t *testing.T) {
	encryptor := &fakeEncryptor{}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: makeTestCred(1, "enc:valid-key"),
		},
	}
	agnes := &fakeAgnesClient{
		generateFn: func(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
			return nil, 0, &timeoutErrFake{}
		},
	}
	svc := NewImageCredentialService(repo, encryptor, agnes, makeTestSchedulerConfig())

	result, err := svc.TestCredential(context.Background(), 1)
	require.NoError(t, err)
	// 超时时 Success=true（key 通过鉴权但响应超时）
	require.True(t, result.Success)
	require.Equal(t, TestCredentialReasonTimeout, result.Reason)
}

// timeoutErrFake 实现 net.Error 接口的 Timeout() 方法
type timeoutErrFake struct{}

func (e *timeoutErrFake) Error() string   { return "i/o timeout" }
func (e *timeoutErrFake) Timeout() bool   { return true }
func (e *timeoutErrFake) Temporary() bool { return false }

// =====================================================================
// 场景 17：errImageCredentialDecryptFailed 可通过 errors.Is 与 infraerrors.ApplicationError 匹配
// =====================================================================

func TestErrImageCredentialDecryptFailed_ErrorMatching(t *testing.T) {
	err := errImageCredentialDecryptFailed().WithCause(errors.New("inner decrypt error"))

	// 验证 errors.Is 匹配（基于 Code + Reason）
	target := infraerrors.New(http.StatusServiceUnavailable, "IMAGE_CREDENTIAL_DECRYPT_FAILED", "")
	require.True(t, errors.Is(err, target),
		"errors.Is should match by Code+Reason for IMAGE_CREDENTIAL_DECRYPT_FAILED")

	// 验证与 IMAGE_PROVIDER_AUTH_FAILED 不匹配
	authFailedTarget := errImageProviderAuthFailed()
	require.False(t, errors.Is(err, authFailedTarget),
		"IMAGE_CREDENTIAL_DECRYPT_FAILED should not match IMAGE_PROVIDER_AUTH_FAILED target")
}

// =====================================================================
// 场景 18：SelectAndGenerate - 凭据不存在时返回 PROVIDER_ERROR
// 这是边界场景：GetByID 返回 NotFound 时不应该被错误映射为 DECRYPT_FAILED
// =====================================================================

func TestGenerateWithCredential_CredentialNotFound_ReturnsProviderError(t *testing.T) {
	encryptor := &fakeEncryptor{}
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{}, // 空 map
	}
	agnes := &fakeAgnesClient{}
	scheduler := NewAgnesCredentialScheduler(repo, agnes, encryptor, nil, makeTestSchedulerConfig())

	_, err := scheduler.GenerateWithCredential(context.Background(), 999, AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	require.Error(t, err)
	appErr, ok := err.(*ImageGenError)
	require.True(t, ok)
	require.Equal(t, "IMAGE_PROVIDER_ERROR", appErr.Reason,
		"credential not found should map to IMAGE_PROVIDER_ERROR, not DECRYPT_FAILED")
}

// =====================================================================
// 场景 19：GenerateWithCredential 凭据禁用时返回 NO_AVAILABLE_CREDENTIAL
// =====================================================================

func TestGenerateWithCredential_DisabledCredential_ReturnsNoAvailableCredential(t *testing.T) {
	encryptor := &fakeEncryptor{}
	disabledCred := makeTestCred(1, "enc:key-1")
	disabledCred.Enabled = false
	repo := &fakeCredRepoForScheduler{
		byID: map[int64]*ImageProviderCredential{
			1: disabledCred,
		},
	}
	agnes := &fakeAgnesClient{}
	scheduler := NewAgnesCredentialScheduler(repo, agnes, encryptor, nil, makeTestSchedulerConfig())

	_, err := scheduler.GenerateWithCredential(context.Background(), 1, AgnesGenerateRequest{
		Model:  "test-model",
		Prompt: "test",
		Size:   "1K",
	})

	require.Error(t, err)
	appErr, ok := err.(*ImageGenError)
	require.True(t, ok)
	require.Equal(t, "IMAGE_NO_AVAILABLE_CREDENTIAL", appErr.Reason)
	require.NotEqual(t, "IMAGE_PROVIDER_AUTH_FAILED", appErr.Reason)
	// AgnesClient 不应被调用
	require.Equal(t, int32(0), atomic.LoadInt32(&agnes.calls))
}

// =====================================================================
// 场景 20：tryDispatch 集成测试 - 解密失败任务正确写为 failed，error_code 透传
// 这验证了端到端：scheduler 返回 IMAGE_CREDENTIAL_DECRYPT_FAILED →
//   tryDispatch 检查 appErr.Reason（非 IMAGE_INVALID_REQUEST）→ 继续重试 →
//   所有重试都失败 → markFailedWithCredential 写入 status=failed, error_code=IMAGE_CREDENTIAL_DECRYPT_FAILED
// =====================================================================

func TestTryDispatch_DecryptFailure_WritesFailedWithDecryptFailedCode(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	cfg.MaxAttemptsPerGeneration = 2
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// 配置 scheduler：占用成功，但每次 GenerateWithCredential 都返回解密失败
	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genErr = errImageCredentialDecryptFailed()

	taskID := int64(500)
	genRepo.claimedStatus[taskID] = ImageStatusQueued

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "test",
		Size:   "2K",
		Ratio:  "1:1",
	}

	// 直接调用 tryDispatch（同步等待完成）
	svc.tryDispatch(taskID, 1, 1, nil, req)

	// 验证：任务被标记为 failed（不是 processing 卡死）
	genRepo.mu.Lock()
	defer genRepo.mu.Unlock()
	require.Equal(t, ImageStatusFailed, genRepo.claimedStatus[taskID],
		"task must be written as 'failed', not stuck in 'processing'")

	// 验证：error_code 透传为 IMAGE_CREDENTIAL_DECRYPT_FAILED（不是 UPSTREAM_FAILED 或 IMAGE_PROVIDER_AUTH_FAILED）
	upd, ok := genRepo.statusUpdates[taskID]
	require.True(t, ok, "should have status update for failed task")
	require.NotNil(t, upd.ErrorCode)
	require.Equal(t, "IMAGE_CREDENTIAL_DECRYPT_FAILED", *upd.ErrorCode,
		"error_code must be IMAGE_CREDENTIAL_DECRYPT_FAILED, not UPSTREAM_FAILED or IMAGE_PROVIDER_AUTH_FAILED")
}

// =====================================================================
// 场景 21：tryDispatch 集成测试 - 解密失败不扣费
// recordUsage 仅在生成成功时扣费，失败路径不调用 recordUsage。
// 这里通过 usageRepo 为 nil 验证失败路径不会 panic（recordUsage 内部 nil check）。
// 真正的扣费验证在集成测试中通过 DeductBalance 调用次数验证。
// =====================================================================

func TestTryDispatch_DecryptFailure_DoesNotPanicWithoutUsageRepo(t *testing.T) {
	cfg := defaultTestImageGenConfig()
	cfg.MaxAttemptsPerGeneration = 1
	svc, genRepo, sched, _ := newTestImageGenService(t, cfg)

	// usageRepo 已为 nil（见 newTestImageGenService）
	sched.acquireOk = true
	sched.hasAvailable = true
	sched.genErr = errImageCredentialDecryptFailed()

	taskID := int64(600)
	genRepo.claimedStatus[taskID] = ImageStatusQueued

	req := CreateGenerationRequest{
		Type:   ImageGenerationTypeTextToImage,
		Prompt: "test",
		Size:   "2K",
		Ratio:  "1:1",
	}

	// 不应 panic
	require.NotPanics(t, func() {
		svc.tryDispatch(taskID, 1, 1, nil, req)
	})

	// 验证任务被标记为 failed
	genRepo.mu.Lock()
	defer genRepo.mu.Unlock()
	require.Equal(t, ImageStatusFailed, genRepo.claimedStatus[taskID])
}
