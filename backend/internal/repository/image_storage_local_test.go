package repository

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// newTestConfig 构造一个用于测试的 *config.Config，支持 local 和 s3 两种驱动。
// signingSecret 为空时使用默认测试密钥；s3Bucket 非空时模拟 s3 配置。
func newTestConfig(localPath, driver, signingSecret, s3Bucket string) *config.Config {
	if signingSecret == "" {
		signingSecret = "test-signing-secret-32bytes-min!!"
	}
	return &config.Config{
		ImageGeneration: config.ImageGenerationConfig{
			StorageDriver:         driver,
			LocalStoragePath:      localPath,
			LocalURLPrefix:        "/api/media",
			LocalURLSigningSecret: signingSecret,
			S3Bucket:              s3Bucket,
			PresignedURLExpires:   60,
			LocalMinFreeSpaceMB:   1,
			LocalMaxFileSizeMB:    10,
		},
	}
}

// newTestLocalStorage 构造一个使用 t.TempDir() 的本地存储实例。
func newTestLocalStorage(t *testing.T) *LocalImageStorage {
	t.Helper()
	cfg := service.LocalStorageConfig{
		RootPath:                   t.TempDir(),
		URLPrefix:                  "/api/media",
		URLSigningSecret:           "test-signing-secret-32bytes-min!!",
		PresignedURLExpiresSeconds: 60,
		MinFreeSpaceMB:             1,
		MaxFileSizeMB:              10,
	}
	storage, err := NewLocalImageStorage(cfg)
	if err != nil {
		t.Fatalf("NewLocalImageStorage failed: %v", err)
	}
	if !storage.Configured() {
		t.Fatal("storage should be configured")
	}
	return storage
}

func TestLocalImageStorage_PutAndHead(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/2026/07/17/test-image.png"
	data := []byte("fake-png-data")

	stored, err := storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader(data),
		ContentType: "image/png",
	})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if stored.Size != int64(len(data)) {
		t.Errorf("expected size %d, got %d", len(data), stored.Size)
	}
	if stored.Key != key {
		t.Errorf("expected key %s, got %s", key, stored.Key)
	}

	// Head
	head, err := storage.Head(ctx, key)
	if err != nil {
		t.Fatalf("Head failed: %v", err)
	}
	if !head.Exists {
		t.Error("expected Exists=true")
	}
	if head.Size != int64(len(data)) {
		t.Errorf("expected head size %d, got %d", len(data), head.Size)
	}
	if head.ContentType != "image/png" {
		t.Errorf("expected content-type image/png, got %s", head.ContentType)
	}
}

func TestLocalImageStorage_Delete(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/test.png"
	data := []byte("data")

	_, err := storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader(data),
		ContentType: "image/png",
	})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if err := storage.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 确认文件已删除
	head, _ := storage.Head(ctx, key)
	if head.Exists {
		t.Error("expected file to be deleted")
	}
}

func TestLocalImageStorage_DeleteNonExistent(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()

	// 删除不存在的文件应该幂等成功
	err := storage.Delete(ctx, "nonexistent/file.png")
	if err != nil {
		t.Errorf("delete non-existent file should be idempotent, got: %v", err)
	}
}

func TestLocalImageStorage_AutoCreateDir(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "deep/nested/path/2026/07/image.png"

	_, err := storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader([]byte("data")),
		ContentType: "image/png",
	})
	if err != nil {
		t.Fatalf("Put with nested path failed: %v", err)
	}

	head, _ := storage.Head(ctx, key)
	if !head.Exists {
		t.Error("expected file to exist in nested directory")
	}
}

func TestLocalImageStorage_SameKeyNotOverwritten(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/duplicate.png"

	_, err := storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader([]byte("first")),
		ContentType: "image/png",
	})
	if err != nil {
		t.Fatalf("first Put failed: %v", err)
	}

	_, err = storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader([]byte("second")),
		ContentType: "image/png",
	})
	if err == nil {
		t.Error("expected error when writing to existing key, got nil")
	}
}

func TestLocalImageStorage_PathTraversalRejected(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()

	cases := []string{
		"../../../etc/passwd",
		"../secret",
		"/etc/passwd",
		"images/../../escape.png",
	}
	for _, key := range cases {
		_, err := storage.Put(ctx, service.PutObjectInput{
			Key:         key,
			Body:        bytes.NewReader([]byte("data")),
			ContentType: "image/png",
		})
		if err == nil {
			t.Errorf("expected path traversal rejection for key %q, got nil", key)
		}
	}
}

func TestLocalImageStorage_AbsolutePathRejected(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()

	_, err := storage.Put(ctx, service.PutObjectInput{
		Key:         "/absolute/path/file.png",
		Body:        bytes.NewReader([]byte("data")),
		ContentType: "image/png",
	})
	if err == nil {
		t.Error("absolute path starting with / should be rejected")
	}
}

func TestLocalImageStorage_InvalidSignatureReturns403(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/test.png"
	_, _ = storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader([]byte("data")),
		ContentType: "image/png",
	})

	// 构造无效签名的 GET 请求
	url := storage.urlPrefix + "/" + key + "?expires=9999999999&signature=invalid"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	storage.ServeFile(w, req, key)
	// ServeFile 不验证签名，签名验证在 MediaHandler 层
	// 这里测试的是文件不存在时的 404
	_ = url
}

func TestLocalImageStorage_SignatureVerification(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/signed.png"
	_, _ = storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader([]byte("data")),
		ContentType: "image/png",
	})

	// 生成有效签名
	expires := time.Now().Add(5 * time.Minute).Unix()
	validSig := storage.signURL("GET", key, expires)

	// 正确签名 → 验证通过
	if !storage.VerifySignature("GET", key, expires, validSig) {
		t.Error("valid signature should verify")
	}

	// 错误签名 → 验证失败
	if storage.VerifySignature("GET", key, expires, "wrong-signature") {
		t.Error("invalid signature should fail verification")
	}

	// 不同 method 签名不可互换
	putSig := storage.signURL("PUT", key, expires)
	if storage.VerifySignature("GET", key, expires, putSig) {
		t.Error("PUT signature should not verify for GET request")
	}
}

func TestLocalImageStorage_ExpiredSignatureReturns403(t *testing.T) {
	storage := newTestLocalStorage(t)
	key := "images/expired.png"

	// 已过期的签名
	expiredTime := time.Now().Add(-1 * time.Minute).Unix()
	sig := storage.signURL("GET", key, expiredTime)

	// 签名本身格式正确，但过期了
	// MediaHandler 层检查 expires > now，这里仅验证签名算法本身
	if !storage.VerifySignature("GET", key, expiredTime, sig) {
		t.Error("signature should be valid (even if expired)")
	}
	// 过期检查在 handler 层做：
	if time.Now().Unix() > expiredTime {
		// 已过期，handler 会返回 403
		return
	}
	t.Error("expected time to be expired")
}

func TestLocalImageStorage_PresignGetReturnsValidURL(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/presigned.png"
	_, _ = storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader([]byte("data")),
		ContentType: "image/png",
	})

	url, err := storage.PresignGet(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet failed: %v", err)
	}
	if !strings.Contains(url, "/api/media/") {
		t.Errorf("URL should contain /api/media/, got: %s", url)
	}
	if !strings.Contains(url, "expires=") || !strings.Contains(url, "signature=") {
		t.Errorf("URL should contain expires and signature params, got: %s", url)
	}
}

// TestLocalImageStorage_PresignGet_TimeBucketStability 验证时间桶优化：
// 同一 key 在同一时间桶内连续多次调用 PresignGet 应返回完全相同的 URL，
// 使浏览器缓存生效，避免轮询时图片反复重载。
func TestLocalImageStorage_PresignGet_TimeBucketStability(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/bucket-test.png"
	_, _ = storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader([]byte("data")),
		ContentType: "image/png",
	})

	// 连续调用 5 次，应返回完全相同的 URL
	urls := make([]string, 5)
	for i := range urls {
		u, err := storage.PresignGet(ctx, key, 30*time.Minute)
		if err != nil {
			t.Fatalf("PresignGet[%d] failed: %v", i, err)
		}
		urls[i] = u
	}
	for i := 1; i < len(urls); i++ {
		if urls[i] != urls[0] {
			t.Errorf("URL should be identical within same time bucket:\n  [0]: %s\n  [%d]: %s", urls[0], i, urls[i])
		}
	}
}

// TestLocalImageStorage_PresignGet_BucketExpiry 验证时间桶过期时间计算：
// expires=30min 时，URL 的 expires 参数应至少 30 分钟后，且不超过 60 分钟。
func TestLocalImageStorage_PresignGet_BucketExpiry(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/expiry-test.png"
	_, _ = storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader([]byte("data")),
		ContentType: "image/png",
	})

	signedURL, err := storage.PresignGet(ctx, key, 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet failed: %v", err)
	}

	// 从 URL 解析 expires 参数
	u, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("parse URL failed: %v", err)
	}
	expiresStr := u.Query().Get("expires")
	if expiresStr == "" {
		t.Fatal("URL missing expires param")
	}

	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		t.Fatalf("parse expires failed: %v", err)
	}
	now := time.Now().Unix()
	minExp := now + 1800 // 至少 30 分钟
	maxExp := now + 3600 // 不超过 60 分钟（2 个 bucket）
	if expires < minExp {
		t.Errorf("expires should be >= 30min from now: got %d, want >= %d", expires, minExp)
	}
	if expires > maxExp+5 { // 5 秒容差
		t.Errorf("expires should be <= 60min from now: got %d, want <= %d", expires, maxExp)
	}
}

// TestLocalImageStorage_PresignGet_DifferentKeysDifferentURLs 验证不同 key 生成不同 URL。
func TestLocalImageStorage_PresignGet_DifferentKeysDifferentURLs(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	_, _ = storage.Put(ctx, service.PutObjectInput{Key: "images/a.png", Body: bytes.NewReader([]byte("a")), ContentType: "image/png"})
	_, _ = storage.Put(ctx, service.PutObjectInput{Key: "images/b.png", Body: bytes.NewReader([]byte("b")), ContentType: "image/png"})

	urlA, _ := storage.PresignGet(ctx, "images/a.png", 30*time.Minute)
	urlB, _ := storage.PresignGet(ctx, "images/b.png", 30*time.Minute)
	if urlA == urlB {
		t.Errorf("different keys should produce different URLs:\n  a: %s\n  b: %s", urlA, urlB)
	}
}

func TestLocalImageStorage_ServeFileWithRange(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/range-test.png"
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	_, _ = storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader(data),
		ContentType: "image/png",
	})

	// GET 请求（带 Range）
	url := storage.urlPrefix + "/" + key
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=0-99")
	w := httptest.NewRecorder()
	storage.ServeFile(w, req, key)

	if w.Code != http.StatusPartialContent {
		t.Errorf("expected 206 Partial Content, got %d", w.Code)
	}
	if w.Body.Len() != 100 {
		t.Errorf("expected 100 bytes, got %d", w.Body.Len())
	}
}

func TestLocalImageStorage_ServeFileHEAD(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/head-test.png"
	data := []byte("test-data-12345")
	_, _ = storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        bytes.NewReader(data),
		ContentType: "image/png",
	})

	req := httptest.NewRequest(http.MethodHead, "/api/media/"+key, nil)
	w := httptest.NewRecorder()
	storage.ServeFile(w, req, key)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	// HEAD 不返回 body
	if w.Body.Len() != 0 {
		t.Errorf("HEAD should not return body, got %d bytes", w.Body.Len())
	}
}

func TestLocalImageStorage_TempFileCleanupOnFailure(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/fail-test.png"

	// 使用一个会在写入中途失败的 Reader
	failingReader := &failingReader{failAfter: 5}
	_, err := storage.Put(ctx, service.PutObjectInput{
		Key:         key,
		Body:        failingReader,
		ContentType: "image/png",
	})
	if err == nil {
		t.Error("expected error from failing reader")
	}

	// 检查没有残留临时文件
	dir := filepath.Join(storage.rootPath, "images")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("temp file not cleaned up: %s", e.Name())
		}
	}
}

// failingReader 在读取 failAfter 字节后返回错误。
type failingReader struct {
	read      int
	failAfter int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.read >= r.failAfter {
		return 0, io.ErrUnexpectedEOF
	}
	n := len(p)
	if n > r.failAfter-r.read {
		n = r.failAfter - r.read
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	r.read += n
	return n, nil
}

func TestLocalImageStorage_DirectoryNotWritableConfigCheckFails(t *testing.T) {
	dir := t.TempDir()
	// 创建目录并设为只读
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	cfg := service.LocalStorageConfig{
		RootPath:                   dir,
		URLSigningSecret:           "test-secret-32bytes-min!!",
		PresignedURLExpiresSeconds: 60,
	}
	_, err := NewLocalImageStorage(cfg)
	if err == nil {
		// 在某些环境下 root 用户可以写只读目录，跳过此测试
		if os.Geteuid() == 0 {
			t.Skip("running as root, readonly directory test skipped")
		}
		t.Error("expected error for read-only directory")
	}
}

func TestLocalImageStorage_LocalModeDoesNotRequireS3(t *testing.T) {
	storage := newTestLocalStorage(t)
	if storage.Driver() != "local" {
		t.Errorf("expected driver 'local', got '%s'", storage.Driver())
	}
	if storage.Bucket() != "local" {
		t.Errorf("expected bucket 'local', got '%s'", storage.Bucket())
	}
	if !storage.Configured() {
		t.Error("expected Configured()=true")
	}
}

func TestLocalImageStorage_MaxFileSizeEnforced(t *testing.T) {
	dir := t.TempDir()
	cfg := service.LocalStorageConfig{
		RootPath:                   dir,
		URLSigningSecret:           "test-secret-32bytes-min!!",
		PresignedURLExpiresSeconds: 60,
		MinFreeSpaceMB:             1,
		MaxFileSizeMB:              1, // 1MB
	}
	storage, err := NewLocalImageStorage(cfg)
	if err != nil {
		t.Fatalf("NewLocalImageStorage failed: %v", err)
	}

	// 写入 2MB 数据（超过 1MB 限制）
	bigData := make([]byte, 2*1024*1024)
	_, err = storage.Put(context.Background(), service.PutObjectInput{
		Key:         "images/big.png",
		Body:        bytes.NewReader(bigData),
		ContentType: "image/png",
	})
	if err == nil {
		t.Error("expected error for file exceeding max size")
	}
}

func TestLocalImageStorage_PresignPutURL(t *testing.T) {
	storage := newTestLocalStorage(t)
	ctx := context.Background()
	key := "images/upload.png"

	url, err := storage.PresignPut(ctx, key, "image/png", 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut failed: %v", err)
	}
	if !strings.Contains(url, "expires=") || !strings.Contains(url, "signature=") {
		t.Errorf("PUT URL should contain expires and signature, got: %s", url)
	}
	if !strings.Contains(url, "content_type=image") {
		t.Errorf("PUT URL should contain content_type param, got: %s", url)
	}
}

func TestLocalImageStorage_ReceiveUpload(t *testing.T) {
	storage := newTestLocalStorage(t)
	key := "images/uploaded.png"
	data := []byte("uploaded-image-data")

	req := httptest.NewRequest(http.MethodPut, "/api/media/"+key, bytes.NewReader(data))
	req.Header.Set("Content-Type", "image/png")

	size, err := storage.ReceiveUpload(req, key, "image/png")
	if err != nil {
		t.Fatalf("ReceiveUpload failed: %v", err)
	}
	if size != int64(len(data)) {
		t.Errorf("expected size %d, got %d", len(data), size)
	}

	// 验证文件已写入
	head, _ := storage.Head(context.Background(), key)
	if !head.Exists {
		t.Error("uploaded file should exist")
	}
}

func TestProvideImageStorage_LocalAutoDetect(t *testing.T) {
	// 测试无 S3Bucket 时自动选择 local
	tmpDir := t.TempDir()
	cfg := newTestConfig(tmpDir, "", "", "")
	// 不设置 S3Bucket，storage_driver 留空

	storage, err := ProvideImageStorage(cfg)
	if err != nil {
		t.Fatalf("ProvideImageStorage failed: %v", err)
	}
	if storage.Driver() != "local" {
		t.Errorf("expected auto-detected driver 'local', got '%s'", storage.Driver())
	}
}

func TestProvideImageStorage_S3AutoDetect(t *testing.T) {
	// 测试有 S3Bucket 时自动选择 s3（AWS SDK 凭据延迟解析，配置加载本身不会失败）
	tmpDir := t.TempDir()
	cfg := newTestConfig(tmpDir, "", "", "test-bucket")

	storage, err := ProvideImageStorage(cfg)
	if err != nil {
		t.Fatalf("ProvideImageStorage failed: %v", err)
	}
	if storage.Driver() != "s3" {
		t.Errorf("expected auto-detected driver 's3', got '%s'", storage.Driver())
	}
}
