//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ---- Test fixtures ----

// agnesChatFakeStorage 是 Agnes 适配器测试用的可控 EnovaImageAssetStorage mock。
type agnesChatFakeStorage struct {
	configured    bool
	putError      error
	presignError  error
	uploadedKeys  []string
	uploadedBodies [][]byte
	presignedKeys  []string
}

func (s *agnesChatFakeStorage) Put(ctx context.Context, input PutObjectInput) (*StoredObject, error) {
	if s.putError != nil {
		return nil, s.putError
	}
	body, _ := io.ReadAll(input.Body)
	s.uploadedKeys = append(s.uploadedKeys, input.Key)
	s.uploadedBodies = append(s.uploadedBodies, append([]byte(nil), body...))
	return &StoredObject{Bucket: "agnes-chat-bucket", Key: input.Key, Size: int64(len(body)), MimeType: input.ContentType}, nil
}

func (s *agnesChatFakeStorage) Delete(ctx context.Context, key string) error { return nil }

func (s *agnesChatFakeStorage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if s.presignError != nil {
		return "", s.presignError
	}
	s.presignedKeys = append(s.presignedKeys, key)
	return "https://r2.example.com/" + key, nil
}

func (s *agnesChatFakeStorage) PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, error) {
	return "https://r2.example.com/put/" + key, nil
}

func (s *agnesChatFakeStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (s *agnesChatFakeStorage) Head(ctx context.Context, key string) (*ObjectHead, error) {
	return nil, errors.New("not implemented")
}

func (s *agnesChatFakeStorage) Bucket() string   { return "agnes-chat-bucket" }
func (s *agnesChatFakeStorage) Configured() bool { return s.configured }
func (s *agnesChatFakeStorage) Driver() string   { return "s3" }

// agnesChatTestConfig 构造 Agnes 适配器测试用的配置。
func agnesChatTestConfig() *config.Config {
	return &config.Config{
		AgnesChat: config.AgnesChatConfig{
			Enabled:             true,
			MaxImagesPerRequest: 6,
			MaxImageBytes:       1024 * 1024, // 1MB
			MaxTotalBytes:       5 * 1024 * 1024,
			R2: config.AgnesChatR2Config{
				Endpoint:              "https://test.r2.cloudflarestorage.com",
				Region:                "auto",
				Bucket:                "agnes-chat-bucket",
				AccessKeyID:           "test-key-id",
				SecretAccessKey:       "test-secret",
				Prefix:                "agnes-chat",
				PublicBaseURL:         "https://r2.example.com",
				PresignExpiresSeconds: 1800,
			},
		},
	}
}

// agnesChatTestAccount 构造启用 Agnes 适配器的测试账号。
func agnesChatTestAccount() *Account {
	return &Account{
		ID:          201,
		Name:        "agnes-chat-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-agnes-test",
			"base_url": "http://upstream.example",
			"model_mapping": map[string]any{
				"agnes-flash-vision": "agnes-2.0-flash",
			},
		},
		Extra: map[string]any{
			ExtraKeyAgnesChatImageAdapter:                    true,
			openai_compat.ExtraKeyResponsesMode:              "force_chat_completions",
		},
	}
}

// makePNGDataURL 构造一个小的有效 PNG data URL（1x1 红点）。
func makePNGDataURL() string {
	// 1x1 红色 PNG（最小有效 PNG）
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x5B, 0x9E, 0xE3,
		0x1B, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
		0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	b64 := base64.StdEncoding.EncodeToString(png)
	return "data:image/png;base64," + b64
}

// ---- Helper function unit tests ----

func TestParseDataURL_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		wantMime   string
		wantB64Sub string
	}{
		{"png", "data:image/png;base64,aGVsbG8=", "image/png", "aGVsbG8="},
		{"jpeg", "data:image/jpeg;base64,aGVsbG8=", "image/jpeg", "aGVsbG8="},
		{"webp", "data:image/webp;base64,aGVsbG8=", "image/webp", "aGVsbG8="},
		{"with charset", "data:image/png;charset=utf-8;base64,aGVsbG8=", "image/png", "aGVsbG8="},
		{"uppercase mime", "data:image/PNG;base64,aGVsbG8=", "image/png", "aGVsbG8="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mime, b64, err := parseDataURL(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.wantMime, mime)
			require.Equal(t, tt.wantB64Sub, b64)
		})
	}
}

func TestParseDataURL_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"not data url", "https://example.com/img.png"},
		{"missing comma", "data:image/png;base64"},
		{"empty data", "data:image/png;base64,"},
		{"not base64", "data:image/png;hex,abcdef"},
		{"missing mime", "data:;base64,aGVsbG8="},
		{"invalid base64 chars", "data:image/png;base64,!!!not-base64!!!"},
		{"length not multiple of 4", "data:image/png;base64,abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseDataURL(tt.input)
			require.Error(t, err)
		})
	}
}

func TestIsValidBase64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"valid", "aGVsbG8=", true},
		{"valid longer", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01, 0x02, 0x03}, 100)), true},
		{"length not mult of 4", "abc", false},
		{"invalid chars", "!!!not-base64!!!", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isValidBase64(tt.input))
		})
	}
}

func TestMimeExtension(t *testing.T) {
	t.Parallel()
	require.Equal(t, ".png", mimeExtension("image/png"))
	require.Equal(t, ".jpg", mimeExtension("image/jpeg"))
	require.Equal(t, ".webp", mimeExtension("image/webp"))
	require.Equal(t, ".bin", mimeExtension("image/gif"))
}

func TestRandomHexKey_Length(t *testing.T) {
	t.Parallel()
	// 16 bytes → 32 hex chars
	k1 := randomHexKey(16)
	require.Len(t, k1, 32)
	// 两次调用应不同（极大概率）
	k2 := randomHexKey(16)
	require.NotEqual(t, k1, k2)
}

// ---- Adapter AdaptBody unit tests ----

func TestAgnesChatImageAdapter_PassesPlainText(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	body := []byte(`{"model":"agnes-flash-vision","messages":[{"role":"user","content":"hello"}],"stream":false}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	out, err := adapter.AdaptBody(ctx, c, body)
	require.NoError(t, err)
	require.Equal(t, body, out, "plain text body should be returned unchanged")
	require.Empty(t, storage.uploadedKeys, "no upload should occur for plain text")
}

func TestAgnesChatImageAdapter_PassesPublicHTTPSURL(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	out, err := adapter.AdaptBody(ctx, c, body)
	require.NoError(t, err)
	require.Equal(t, body, out, "public HTTPS URL should be passed through unchanged")
	require.Empty(t, storage.uploadedKeys, "no upload should occur for public HTTPS URL")
}

func TestAgnesChatImageAdapter_ReplacesDataURL(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	dataURL := makePNGDataURL()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	out, err := adapter.AdaptBody(ctx, c, body)
	require.NoError(t, err)
	require.NotEqual(t, body, out, "body should be mutated when data URL is replaced")

	// data URL should no longer be present
	require.NotContains(t, string(out), "data:image")
	// replaced URL should be the R2 public URL
	url := gjson.GetBytes(out, "messages.0.content.1.image_url.url").String()
	require.True(t, strings.HasPrefix(url, "https://r2.example.com/"), "expected R2 URL, got %s", url)
	// key should contain user_id prefix
	require.Contains(t, url, "/42/")
	// storage should have received exactly one upload
	require.Len(t, storage.uploadedKeys, 1)
	require.Len(t, storage.presignedKeys, 1)
	require.Equal(t, storage.uploadedKeys[0], storage.presignedKeys[0])
}

func TestAgnesChatImageAdapter_MixedDataURLAndHTTPSURL(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	dataURL := makePNGDataURL()
	publicURL := "https://example.com/public.jpg"
	// 顺序：data URL 先，public URL 后
	body := []byte(`{"model":"x","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"describe these"},` +
		`{"type":"image_url","image_url":{"url":"` + dataURL + `"}},` +
		`{"type":"image_url","image_url":{"url":"` + publicURL + `"}}` +
		`]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(7))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	out, err := adapter.AdaptBody(ctx, c, body)
	require.NoError(t, err)

	url1 := gjson.GetBytes(out, "messages.0.content.1.image_url.url").String()
	url2 := gjson.GetBytes(out, "messages.0.content.2.image_url.url").String()
	// data URL → R2
	require.True(t, strings.HasPrefix(url1, "https://r2.example.com/"), "first URL should be R2, got %s", url1)
	require.NotContains(t, url1, "data:image")
	// public URL → unchanged
	require.Equal(t, publicURL, url2, "second URL should be unchanged")
	// only one upload
	require.Len(t, storage.uploadedKeys, 1)
}

func TestAgnesChatImageAdapter_RejectsFileScheme(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"file:///etc/passwd"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	out, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusBadRequest, adapterErr.StatusCode)
	require.Equal(t, "invalid_request_error", adapterErr.ErrType)
	require.Equal(t, body, out, "body should be returned unchanged on error")
	require.Empty(t, storage.uploadedKeys, "no upload should occur on rejection")
}

func TestAgnesChatImageAdapter_RejectsBlobScheme(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"blob:https://example.com/uuid"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusBadRequest, adapterErr.StatusCode)
}

func TestAgnesChatImageAdapter_RejectsHTTPScheme(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://example.com/img.png"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusBadRequest, adapterErr.StatusCode)
}

func TestAgnesChatImageAdapter_RejectsMalformedDataURL(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	tests := []struct {
		name string
		url  string
	}{
		{"missing comma", "data:image/png;base64"},
		{"empty data", "data:image/png;base64,"},
		{"not base64", "data:image/png;hex,abcdef"},
		{"invalid base64 chars", "data:image/png;base64,!!!"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + tt.url + `"}}]}]}`)

			ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())

			_, err := adapter.AdaptBody(ctx, c, body)
			require.Error(t, err)
			var adapterErr *AgnesChatImageAdapterError
			require.ErrorAs(t, err, &adapterErr)
			require.Equal(t, http.StatusBadRequest, adapterErr.StatusCode)
			require.Equal(t, "invalid_request_error", adapterErr.ErrType)
		})
	}
}

func TestAgnesChatImageAdapter_RejectsUnsupportedMime(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/gif;base64,aGVsbG8="}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusBadRequest, adapterErr.StatusCode)
	require.Contains(t, adapterErr.Message, "image/gif")
}

func TestAgnesChatImageAdapter_RejectsOversizeImage(t *testing.T) {
	t.Parallel()
	cfg := agnesChatTestConfig()
	cfg.AgnesChat.MaxImageBytes = 10 // 极小限制
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, cfg)

	// 构造一个超过 10 字节的 PNG data URL
	dataURL := makePNGDataURL()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusBadRequest, adapterErr.StatusCode)
	require.Contains(t, adapterErr.Message, "max size")
	require.Empty(t, storage.uploadedKeys, "no upload should occur on oversize rejection")
}

func TestAgnesChatImageAdapter_RejectsTooManyImages(t *testing.T) {
	t.Parallel()
	cfg := agnesChatTestConfig()
	cfg.AgnesChat.MaxImagesPerRequest = 1
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, cfg)

	dataURL := makePNGDataURL()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + dataURL + `"}},` +
		`{"type":"image_url","image_url":{"url":"` + dataURL + `"}}` +
		`]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusBadRequest, adapterErr.StatusCode)
	require.Contains(t, adapterErr.Message, "too many images")
}

func TestAgnesChatImageAdapter_R2UploadFailure(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{
		configured: true,
		putError:   errors.New("R2 throttled"),
	}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	dataURL := makePNGDataURL()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusBadGateway, adapterErr.StatusCode)
	require.Equal(t, "api_error", adapterErr.ErrType)
	// 不泄露内部错误细节
	require.NotContains(t, adapterErr.Message, "R2 throttled")
}

func TestAgnesChatImageAdapter_StorageNotConfigured(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: false}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	dataURL := makePNGDataURL()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusServiceUnavailable, adapterErr.StatusCode)
}

func TestAgnesChatImageAdapter_PresignFailureCleansUp(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{
		configured:   true,
		presignError: errors.New("presign failed"),
	}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	dataURL := makePNGDataURL()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusBadGateway, adapterErr.StatusCode)
	// 上传成功但 presign 失败：应已上传一次
	require.Len(t, storage.uploadedKeys, 1)
}

func TestAgnesChatImageAdapter_NilAdapterIsNoop(t *testing.T) {
	t.Parallel()
	var adapter *AgnesChatImageAdapter
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)
	ctx := context.Background()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	out, err := adapter.AdaptBody(ctx, c, body)
	require.NoError(t, err)
	require.Equal(t, body, out)
}

// ---- E2E test through forwardAsRawChatCompletions ----

func TestForwardAsRawChatCompletions_AgnesChatImageAdapter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dataURL := makePNGDataURL()
	publicURL := "https://example.com/public.jpg"
	// 注意：data URL 中的 base64 不含 /，避免 gjson 解析问题
	body := []byte(`{"model":"agnes-flash-vision","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"describe these images"},` +
		`{"type":"image_url","image_url":{"url":"` + dataURL + `"}},` +
		`{"type":"image_url","image_url":{"url":"` + publicURL + `"}}` +
		`]}],"stream":false}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	// 设置 userID 到 context
	ctx := context.WithValue(c.Request.Context(), ctxkey.UserID, int64(42))
	c.Request = c.Request.WithContext(ctx)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_agnes","object":"chat.completion","model":"agnes-2.0-flash","choices":[{"index":0,"message":{"role":"assistant","content":"a red dot and a photo"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":8,"total_tokens":13}}`,
		)),
	}}

	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	svc := &OpenAIGatewayService{
		cfg:                   rawChatCompletionsTestConfig(),
		httpUpstream:          upstream,
		agnesChatImageAdapter: adapter,
	}
	account := agnesChatTestAccount()

	result, err := svc.forwardAsRawChatCompletions(ctx, c, account, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)

	// 断言上游收到的是 POST /v1/chat/completions
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)

	// 断言 model 已映射为 agnes-2.0-flash
	require.Equal(t, "agnes-2.0-flash", gjson.GetBytes(upstream.lastBody, "model").String())

	// 断言 messages[0].content 数组保留 text + image_url 结构
	contentArr := gjson.GetBytes(upstream.lastBody, "messages.0.content")
	require.True(t, contentArr.IsArray(), "content should be array")
	parts := contentArr.Array()
	require.Len(t, parts, 3)
	require.Equal(t, "text", parts[0].Get("type").String())
	require.Equal(t, "describe these images", parts[0].Get("text").String())
	require.Equal(t, "image_url", parts[1].Get("type").String())
	require.Equal(t, "image_url", parts[2].Get("type").String())

	// 断言 data URL 已被替换为 R2 HTTPS URL
	url1 := parts[1].Get("image_url.url").String()
	require.True(t, strings.HasPrefix(url1, "https://r2.example.com/"), "data URL should be replaced with R2 URL, got %s", url1)
	require.NotContains(t, url1, "data:image")
	require.Contains(t, url1, "/42/")

	// 断言公网 HTTPS URL 未被替换
	url2 := parts[2].Get("image_url.url").String()
	require.Equal(t, publicURL, url2, "public HTTPS URL should be unchanged")

	// 断言存储只上传了一次（data URL），公网 URL 不上传
	require.Len(t, storage.uploadedKeys, 1)

	// 断言上游请求头 Authorization 为 Bearer
	require.Equal(t, "Bearer sk-agnes-test", upstream.lastReq.Header.Get("Authorization"))
}

// ---- E2E test: streaming ----

func TestForwardAsRawChatCompletions_AgnesChatImageAdapter_Streaming(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dataURL := makePNGDataURL()
	body := []byte(`{"model":"agnes-flash-vision","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"describe"},` +
		`{"type":"image_url","image_url":{"url":"` + dataURL + `"}}` +
		`]}],"stream":true}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(c.Request.Context(), ctxkey.UserID, int64(42))
	c.Request = c.Request.WithContext(ctx)

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"agnes-2.0-flash","choices":[{"index":0,"delta":{"content":"red dot"}}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"agnes-2.0-flash","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	svc := &OpenAIGatewayService{
		cfg:                   rawChatCompletionsTestConfig(),
		httpUpstream:          upstream,
		agnesChatImageAdapter: adapter,
	}
	account := agnesChatTestAccount()

	result, err := svc.forwardAsRawChatCompletions(ctx, c, account, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, "agnes-2.0-flash", gjson.GetBytes(upstream.lastBody, "model").String())

	// streaming body 应包含 R2 URL
	require.NotContains(t, string(upstream.lastBody), "data:image")
	require.Contains(t, string(upstream.lastBody), "https://r2.example.com/")
	require.Len(t, storage.uploadedKeys, 1)
	require.Contains(t, rec.Body.String(), "red dot")
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

// ---- E2E test: Agnes upstream error ----

func TestForwardAsRawChatCompletions_AgnesChatImageAdapter_UpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dataURL := makePNGDataURL()
	body := []byte(`{"model":"agnes-flash-vision","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + dataURL + `"}}` +
		`]}],"stream":false}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(c.Request.Context(), ctxkey.UserID, int64(42))
	c.Request = c.Request.WithContext(ctx)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Agnes internal error","type":"server_error"}}`)),
	}}

	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	svc := &OpenAIGatewayService{
		cfg:                   rawChatCompletionsTestConfig(),
		httpUpstream:          upstream,
		agnesChatImageAdapter: adapter,
	}
	account := agnesChatTestAccount()

	_, err := svc.forwardAsRawChatCompletions(ctx, c, account, body, "")
	require.Error(t, err)
	// data URL 已替换为 R2 URL 再发给上游
	require.NotContains(t, string(upstream.lastBody), "data:image")
	require.Contains(t, string(upstream.lastBody), "https://r2.example.com/")
}

// ---- E2E test: invalid data URL returns 400 ----

func TestForwardAsRawChatCompletions_AgnesChatImageAdapter_InvalidDataURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"agnes-flash-vision","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;hex,abcdef"}}` +
		`]}],"stream":false}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(c.Request.Context(), ctxkey.UserID, int64(42))
	c.Request = c.Request.WithContext(ctx)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}}

	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	svc := &OpenAIGatewayService{
		cfg:                   rawChatCompletionsTestConfig(),
		httpUpstream:          upstream,
		agnesChatImageAdapter: adapter,
	}
	account := agnesChatTestAccount()

	_, err := svc.forwardAsRawChatCompletions(ctx, c, account, body, "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_request_error")
	// 上游不应被调用
	require.Nil(t, upstream.lastReq)
}

// ---- E2E test: account without adapter flag is unaffected ----

func TestForwardAsRawChatCompletions_AccountWithoutAdapterFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"x","object":"chat.completion","model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		)),
	}}

	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	svc := &OpenAIGatewayService{
		cfg:                   rawChatCompletionsTestConfig(),
		httpUpstream:          upstream,
		agnesChatImageAdapter: adapter,
	}
	// 普通账号（无 Agnes 适配器标记）
	account := rawChatCompletionsTestAccount()

	_, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
	require.NoError(t, err)
	// 普通账号不受影响：body 原样转发
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Empty(t, storage.uploadedKeys, "no upload should occur for non-Agnes account")
}

// ---- 回归测试：修复 3 个 important 问题 ----

// TestAgnesChatR2Config_IsConfigured_RequiresEndpoint 验证 endpoint 缺失时
// IsConfigured() 返回 false（修复 issue #1）。
func TestAgnesChatR2Config_IsConfigured_RequiresEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfg      config.AgnesChatR2Config
		expected bool
	}{
		{
			name:     "all empty",
			cfg:      config.AgnesChatR2Config{},
			expected: false,
		},
		{
			name: "missing endpoint",
			cfg: config.AgnesChatR2Config{
				Bucket:          "agnes-chat",
				AccessKeyID:     "key-id",
				SecretAccessKey: "secret",
				// Endpoint 故意留空
			},
			expected: false,
		},
		{
			name: "missing bucket",
			cfg: config.AgnesChatR2Config{
				Endpoint:        "https://r2.cloudflarestorage.com",
				AccessKeyID:     "key-id",
				SecretAccessKey: "secret",
			},
			expected: false,
		},
		{
			name: "all set",
			cfg: config.AgnesChatR2Config{
				Endpoint:        "https://r2.cloudflarestorage.com",
				Bucket:          "agnes-chat",
				AccessKeyID:     "key-id",
				SecretAccessKey: "secret",
			},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.cfg.IsConfigured())
		})
	}
}

// TestAgnesChatImageAdapter_TextWithKeywordsNotTreatedAsImage 验证纯文本消息中
// 出现 "image_url" 和 "data:image" 关键字时不会被误判为图片请求（修复 issue #2）。
func TestAgnesChatImageAdapter_TextWithKeywordsNotTreatedAsImage(t *testing.T) {
	t.Parallel()
	// 存储未配置，但纯文本消息中包含 image_url 与 data:image 关键字
	storage := &agnesChatFakeStorage{configured: false}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	body := []byte(`{"model":"agnes-flash-vision","messages":[` +
		`{"role":"user","content":"请解释 image_url 和 data:image 的区别，以及它们在 OpenAI API 中的用法"},` +
		`{"role":"assistant","content":"image_url 是 OpenAI 多模态 API 中的字段，data:image 是 base64 图片前缀..."}` +
		`]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	out, err := adapter.AdaptBody(ctx, c, body)
	// 关键：不应返回 503，应原样透传
	require.NoError(t, err, "text with keywords should not be treated as image request")
	require.Equal(t, body, out, "body should be returned unchanged for text-only messages")
}

// TestAgnesChatImageAdapter_HasDataURLImageBlock_TextOnly 验证结构化检测函数
// 对纯文本消息返回 false。
func TestAgnesChatImageAdapter_HasDataURLImageBlock_TextOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "text contains image_url keyword",
			body: `{"messages":[{"role":"user","content":"explain image_url field"}]}`,
			want: false,
		},
		{
			name: "text contains data:image keyword",
			body: `{"messages":[{"role":"user","content":"what does data:image mean?"}]}`,
			want: false,
		},
		{
			name: "text contains both keywords",
			body: `{"messages":[{"role":"user","content":"image_url vs data:image"}]}`,
			want: false,
		},
		{
			name: "real data url image block",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`,
			want: true,
		},
		{
			name: "real data url in second message",
			body: `{"messages":[{"role":"user","content":"hi"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,aGVsbG8="}}]}]}`,
			want: true,
		},
		{
			name: "public https url image block",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}]}]}`,
			want: false,
		},
		{
			name: "non-image_url typed block with data:image",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"data:image/png;base64,aGVsbG8="}]}]}`,
			want: false,
		},
		{
			name: "empty messages",
			body: `{"messages":[]}`,
			want: false,
		},
		{
			name: "no messages field",
			body: `{"model":"x"}`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, hasDataURLImageBlock([]byte(tt.body)))
		})
	}
}

// TestAgnesChatImageAdapter_RejectsForgedImageMagicBytes 验证伪造 MIME 类型
// 的非图片二进制内容会被拒绝（修复 issue #3）。
func TestAgnesChatImageAdapter_RejectsForgedImageMagicBytes(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	tests := []struct {
		name     string
		declared string // 声明的 MIME
		content  []byte // 实际二进制内容
	}{
		{
			name:     "html forged as png",
			declared: "image/png",
			content:  []byte("<html><body><script>alert('xss')</script></body></html>"),
		},
		{
			name:     "executable forged as jpeg",
			declared: "image/jpeg",
			content:  []byte{0x7F, 0x45, 0x4C, 0x46, 0x02, 0x01, 0x01, 0x00}, // ELF header
		},
		{
			name:     "plain text forged as webp",
			declared: "image/webp",
			content:  []byte("not a webp file at all, just text"),
		},
		{
			name:     "png content but declared as jpeg",
			declared: "image/jpeg",
			content:  []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, // PNG magic
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b64 := base64.StdEncoding.EncodeToString(tt.content)
			dataURL := "data:" + tt.declared + ";base64," + b64
			body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)

			_, err := adapter.AdaptBody(ctx, c, body)
			require.Error(t, err)
			var adapterErr *AgnesChatImageAdapterError
			require.ErrorAs(t, err, &adapterErr)
			require.Equal(t, http.StatusBadRequest, adapterErr.StatusCode, "forged image should be rejected as 400")
			require.Equal(t, "invalid_request_error", adapterErr.ErrType)
			// 不应泄露内部实现细节（如 magic bytes 表）
			require.NotContains(t, adapterErr.Message, "R2")
			require.NotContains(t, adapterErr.Message, "throttled")
		})
	}
}

// TestAgnesChatImageAdapter_AcceptsValidMagicBytes 验证真实 PNG/JPEG/WebP
// 文件签名能通过 magic bytes 校验。
func TestAgnesChatImageAdapter_AcceptsValidMagicBytes(t *testing.T) {
	t.Parallel()
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, agnesChatTestConfig())

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	tests := []struct {
		name     string
		declared string
		content  []byte
	}{
		{
			name:     "valid png",
			declared: "image/png",
			content: []byte{
				0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
				0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
			},
		},
		{
			name:     "valid jpeg",
			declared: "image/jpeg",
			content: []byte{
				0xFF, 0xD8, 0xFF, 0xE0, // SOI + APP0 marker
				0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, // JFIF identifier
			},
		},
		{
			name:     "valid webp",
			declared: "image/webp",
			content: []byte{
				0x52, 0x49, 0x46, 0x46, // "RIFF"
				0x00, 0x00, 0x00, 0x00, // file size (placeholder)
				0x57, 0x45, 0x42, 0x50, // "WEBP"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b64 := base64.StdEncoding.EncodeToString(tt.content)
			dataURL := "data:" + tt.declared + ";base64," + b64
			body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)

			out, err := adapter.AdaptBody(ctx, c, body)
			require.NoError(t, err, "valid %s should pass magic bytes check", tt.declared)
			// 应该已上传到 R2
			require.Len(t, storage.uploadedKeys, 1, "image should be uploaded to R2")
			// body 中的 data URL 应被替换为 R2 URL
			require.NotContains(t, string(out), "data:image")
			url := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String()
			require.True(t, strings.HasPrefix(url, "https://r2.example.com/"), "expected R2 URL, got %s", url)
			// 重置 storage 状态供下一个子测试使用
			storage.uploadedKeys = nil
			storage.presignedKeys = nil
			storage.uploadedBodies = nil
		})
	}
}

// TestVerifyImageMagicBytes_InvalidBase64 验证 base64 解码失败时的处理。
func TestVerifyImageMagicBytes_InvalidBase64(t *testing.T) {
	t.Parallel()
	// 长度不是 4 的倍数且向下取整后为 0
	err := verifyImageMagicBytes("abc", "image/png")
	require.Error(t, err)
}

// TestAgnesChatImageAdapter_EndpointMissingReturns503 验证 endpoint 缺失时
// 配置不视为就绪，data URL 请求返回 503 而非 502（修复 issue #1 的运行时行为）。
func TestAgnesChatImageAdapter_EndpointMissingReturns503(t *testing.T) {
	t.Parallel()
	// 模拟 endpoint 缺失：cfg.Active() 返回 false
	cfg := agnesChatTestConfig()
	cfg.AgnesChat.R2.Endpoint = "" // 清空 endpoint
	// 此处 cfg.Active() 应为 false，因为 IsConfigured() 要求 endpoint 非空
	require.False(t, cfg.AgnesChat.Active(), "config without endpoint should not be active")

	// storage.Configured()=true（模拟 S3 客户端已创建），但 cfg.Active()=false
	// 适配器应使用 cfg.Active() 判断，返回 503
	storage := &agnesChatFakeStorage{configured: true}
	adapter := ProvideAgnesChatImageAdapter(storage, cfg)

	dataURL := makePNGDataURL()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)

	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, err := adapter.AdaptBody(ctx, c, body)
	require.Error(t, err)
	var adapterErr *AgnesChatImageAdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, http.StatusServiceUnavailable, adapterErr.StatusCode, "missing endpoint should return 503, not 502")
	require.Empty(t, storage.uploadedKeys, "no upload should be attempted when storage is not ready")
}
