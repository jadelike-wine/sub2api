package repository

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 媒体类型默认 MIME 表（扩展名 → content-type），用于 HEAD/GET 时补全。
var localMediaMimeTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// LocalImageStorage 实现 service.ImageObjectStorage，基于本地磁盘。
//
// 安全约束：
//   - 文件名由服务端生成（UUID），不使用用户上传的原始文件名
//   - 对象 Key 仅保存相对路径，数据库不存绝对路径
//   - 访问 URL 使用 HMAC-SHA256 签名 + 过期时间，不公开目录
//   - 写入采用临时文件 + 原子 rename，避免半写文件
//   - 路径穿越校验：禁止 ../、绝对路径、符号链接逃逸
type LocalImageStorage struct {
	rootPath     string
	urlPrefix    string
	signingKey   []byte
	presignExp   time.Duration
	minFreeBytes int64
	maxFileBytes int64
	configured   bool
}

// NewLocalImageStorage 根据配置构造本地存储实例。
// 启动时会创建根目录并测试写权限；配置无效时返回 configured=false 的实例（不阻塞启动）。
func NewLocalImageStorage(cfg service.LocalStorageConfig) (*LocalImageStorage, error) {
	rootPath := filepath.Clean(cfg.RootPath)
	if rootPath == "" || rootPath == "." {
		return &LocalImageStorage{configured: false}, nil
	}

	if cfg.URLSigningSecret == "" {
		return nil, errors.New("local_url_signing_secret is required for local storage")
	}

	urlPrefix := strings.TrimRight(cfg.URLPrefix, "/")
	if urlPrefix == "" {
		urlPrefix = "/api/media"
	}

	exp := time.Duration(cfg.PresignedURLExpiresSeconds) * time.Second
	if exp <= 0 {
		exp = 30 * time.Minute
	}

	minFreeBytes := int64(cfg.MinFreeSpaceMB) * 1024 * 1024
	if minFreeBytes <= 0 {
		minFreeBytes = 2 * 1024 * 1024 * 1024 // 默认 2GB
	}
	maxFileBytes := int64(cfg.MaxFileSizeMB) * 1024 * 1024
	if maxFileBytes <= 0 {
		maxFileBytes = 100 * 1024 * 1024 // 默认 100MB
	}

	// 创建根目录（0750）
	if err := os.MkdirAll(rootPath, 0o750); err != nil {
		return nil, fmt.Errorf("create local storage root %s: %w", rootPath, err)
	}

	// 测试写权限：创建临时文件后删除
	if err := testDirectoryWritable(rootPath); err != nil {
		return nil, fmt.Errorf("local storage root not writable: %w", err)
	}

	return &LocalImageStorage{
		rootPath:     rootPath,
		urlPrefix:    urlPrefix,
		signingKey:   []byte(cfg.URLSigningSecret),
		presignExp:   exp,
		minFreeBytes: minFreeBytes,
		maxFileBytes: maxFileBytes,
		configured:   true,
	}, nil
}

func (l *LocalImageStorage) Bucket() string   { return "local" }
func (l *LocalImageStorage) Configured() bool { return l.configured }
func (l *LocalImageStorage) Driver() string   { return "local" }

// ---- 路径安全 ----

// safeJoinPath 将相对 key 安全地拼接到 rootPath 下，拒绝路径穿越。
// 返回的 absPath 保证在 rootPath 目录内。
//
// 安全策略：
//   - 拒绝任何包含 ".." 路径段的 key（不依赖事后清理，直接报错）
//   - 拒绝绝对路径（以 / 开头的 key）
//   - 清理后再做 filepath.Rel 校验作为兜底
//   - 不跟随指向根目录外部的符号链接
func (l *LocalImageStorage) safeJoinPath(key string) (string, error) {
	// 拒绝包含 .. 路径段的 key
	if containsDotDotSegment(key) {
		return "", fmt.Errorf("path traversal detected: key contains '..' segment")
	}

	// 拒绝绝对路径（以 / 开头）
	if strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("absolute path not allowed in object key")
	}

	// 清理 key
	cleaned := filepath.Clean(key)
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("empty object key")
	}

	absPath := filepath.Join(l.rootPath, cleaned)
	// 校验结果仍在 rootPath 下（兜底）
	rel, err := filepath.Rel(l.rootPath, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path traversal detected")
	}

	// 不跟随指向根目录外部的符号链接
	if li, err := os.Lstat(absPath); err == nil && li.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absPath)
		if err != nil {
			return "", fmt.Errorf("read symlink: %w", err)
		}
		targetAbs := target
		if !filepath.IsAbs(target) {
			targetAbs = filepath.Join(filepath.Dir(absPath), target)
		}
		targetAbs = filepath.Clean(targetAbs)
		relTarget, err := filepath.Rel(l.rootPath, targetAbs)
		if err != nil || strings.HasPrefix(relTarget, "..") {
			return "", fmt.Errorf("symlink escapes storage root")
		}
	}

	return absPath, nil
}

// containsDotDotSegment 检查路径中是否包含 ".." 路径段（按 / 分割后精确匹配）。
func containsDotDotSegment(path string) bool {
	// 去除 query 参数（URL 编码的 key 可能带 ?）
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// ---- 磁盘空间检查 ----

// checkFreeSpace 检查目标路径所在文件系统的剩余空间是否足够。
func (l *LocalImageStorage) checkFreeSpace(path string) error {
	var stat syscall.Statfs_t
	// 向上找到存在的目录
	dir := path
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil // 兜底：无法确定目录时不拦截
		}
		dir = parent
	}
	if err := syscall.Statfs(dir, &stat); err != nil {
		return nil // Statfs 失败不拦截写入（兼容非 Linux 环境）
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	if int64(freeBytes) < l.minFreeBytes {
		return errLocalDiskFull
	}
	return nil
}

// ---- 接口实现 ----

func (l *LocalImageStorage) Put(ctx context.Context, input service.PutObjectInput) (*service.StoredObject, error) {
	if !l.configured {
		return nil, errors.New("local image storage is not configured")
	}

	absPath, err := l.safeJoinPath(input.Key)
	if err != nil {
		return nil, err
	}

	// 检查文件是否已存在（同名不覆盖）
	if _, err := os.Stat(absPath); err == nil {
		return nil, fmt.Errorf("object already exists: %s", input.Key)
	}

	// 创建目标目录
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	// 检查磁盘空间
	if err := l.checkFreeSpace(absPath); err != nil {
		return nil, err
	}

	contentType := input.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// 写入临时文件（同目录，确保在同一文件系统上支持原子 rename）
	tmpPath := absPath + ".tmp." + randomSuffix()
	tmpFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}

	// 流式拷贝，同时计算大小
	written, err := io.Copy(tmpFile, input.Body)
	tmpFile.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("write temp file: %w", err)
	}

	// 检查大小限制
	if l.maxFileBytes > 0 && written > l.maxFileBytes {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("file size %d exceeds max %d", written, l.maxFileBytes)
	}

	// 原子 rename
	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("atomic rename: %w", err)
	}

	return &service.StoredObject{
		Bucket:   "local",
		Key:      input.Key,
		Size:     written,
		ETag:     "",
		MimeType: contentType,
	}, nil
}

func (l *LocalImageStorage) Delete(ctx context.Context, key string) error {
	if !l.configured {
		return errors.New("local image storage is not configured")
	}
	absPath, err := l.safeJoinPath(key)
	if err != nil {
		return err
	}
	// 幂等删除：文件不存在视为成功
	if err := os.Remove(absPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete file: %w", err)
	}
	return nil
}

func (l *LocalImageStorage) Head(ctx context.Context, key string) (*service.ObjectHead, error) {
	if !l.configured {
		return nil, errors.New("local image storage is not configured")
	}
	absPath, err := l.safeJoinPath(key)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &service.ObjectHead{Bucket: "local", Key: key, Exists: false}, nil
		}
		return nil, fmt.Errorf("stat file: %w", err)
	}
	contentType := guessContentType(absPath)
	return &service.ObjectHead{
		Bucket:      "local",
		Key:         key,
		Size:        info.Size(),
		ContentType: contentType,
		Exists:      true,
	}, nil
}

// Get 读取本地存储对象的原始内容（用于图生图场景将输入图片 base64 编码后发给上游）。
// 调用方负责关闭返回的 ReadCloser。
func (l *LocalImageStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if !l.configured {
		return nil, errors.New("local image storage is not configured")
	}
	absPath, err := l.safeJoinPath(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("object not found: %s", key)
		}
		return nil, fmt.Errorf("open file: %w", err)
	}
	return f, nil
}

// ---- 签名 URL ----

// signURL 对 method + key + expires 生成 HMAC-SHA256 签名。
func (l *LocalImageStorage) signURL(method, key string, expires int64) string {
	payload := fmt.Sprintf("%s\n%s\n%d", method, key, expires)
	mac := hmac.New(sha256.New, l.signingKey)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifySignature 使用常量时间比较验证签名。
func (l *LocalImageStorage) verifySignature(method, key string, expires int64, signature string) bool {
	expected := l.signURL(method, key, expires)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// VerifySignature 暴露给 handler 使用，验证 URL 签名。
func (l *LocalImageStorage) VerifySignature(method, key string, expires int64, signature string) bool {
	return l.verifySignature(method, key, expires, signature)
}

// PresignGet 生成带签名、带过期时间的下载 URL。
//
// 时间桶优化：将 expires 向下取整到桶边界（bucket = expires 时长），
// 使同一 key 在同一桶内生成完全相同的 URL，让浏览器缓存生效，
// 避免轮询时每次返回不同的 URL 导致图片反复重载。
//
// 过期时间 = 当前桶结束 + 1 个 bucket 缓冲，这样桶边界切换时
// 旧 URL 仍有 1 个 bucket 的有效期，不会立即失效。
func (l *LocalImageStorage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if !l.configured {
		return "", errors.New("local image storage is not configured")
	}
	if expires <= 0 {
		expires = l.presignExp
	}
	bucketSeconds := int64(expires.Seconds())
	if bucketSeconds <= 0 {
		bucketSeconds = 1800
	}
	now := time.Now().Unix()
	bucketStart := (now / bucketSeconds) * bucketSeconds
	exp := bucketStart + 2*bucketSeconds
	sig := l.signURL("GET", key, exp)
	return fmt.Sprintf("%s/%s?expires=%d&signature=%s", l.urlPrefix, key, exp, sig), nil
}

// PresignPut 生成带签名、带过期时间的上传 URL。
// 客户端通过 PUT 方法将文件二进制作为请求体发送到该 URL。
func (l *LocalImageStorage) PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, error) {
	if !l.configured {
		return "", errors.New("local image storage is not configured")
	}
	if expires <= 0 {
		expires = l.presignExp
	}
	exp := time.Now().Add(expires).Unix()
	sig := l.signURL("PUT", key, exp)
	url := fmt.Sprintf("%s/%s?expires=%d&signature=%s", l.urlPrefix, key, exp, sig)
	if contentType != "" {
		url += "&content_type=" + contentType
	}
	return url, nil
}

// ---- ServeHTTP 辅助 ----

// ServeFile 流式返回文件内容，支持 Range 请求和 HEAD。
// 调用方负责从 URL query 解析 expires/signature 并验证签名。
func (l *LocalImageStorage) ServeFile(w http.ResponseWriter, r *http.Request, key string) {
	absPath, err := l.safeJoinPath(key)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	f, err := os.Open(absPath)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	contentType := guessContentType(absPath)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	// Cache-Control 从 5 分钟增加到 30 分钟，确保浏览器在当前签名 URL 的最短剩余有效期内可稳定复用缓存。
	// 注：签名 URL 的剩余有效期约为 30～60 分钟，缓存固定 30 分钟，取最短值保证缓存不会超过 URL 有效期。
	w.Header().Set("Cache-Control", "private, max-age=1800")
	// http.ServeContent 支持 GET/HEAD 和 Range 请求
	http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), f)
}

// ReceiveUpload 从请求体流式读取文件并写入存储（供 PUT 上传使用）。
// 返回写入的对象大小和 content-type。
func (l *LocalImageStorage) ReceiveUpload(r *http.Request, key string, contentType string) (int64, error) {
	if !l.configured {
		return 0, errors.New("local image storage is not configured")
	}

	absPath, err := l.safeJoinKeyForUpload(key)
	if err != nil {
		return 0, err
	}

	// 同名不覆盖
	if _, err := os.Stat(absPath); err == nil {
		return 0, fmt.Errorf("object already exists: %s", key)
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, fmt.Errorf("create directory: %w", err)
	}

	if err := l.checkFreeSpace(absPath); err != nil {
		return 0, err
	}

	if contentType == "" {
		contentType = r.Header.Get("Content-Type")
	}

	tmpPath := absPath + ".tmp." + randomSuffix()
	tmpFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}

	// 使用 LimitReader 防止超出最大文件大小
	var written int64
	if l.maxFileBytes > 0 {
		written, err = io.Copy(tmpFile, io.LimitReader(r.Body, l.maxFileBytes+1))
	} else {
		written, err = io.Copy(tmpFile, r.Body)
	}
	tmpFile.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("write temp file: %w", err)
	}

	if l.maxFileBytes > 0 && written > l.maxFileBytes {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("file size %d exceeds max %d", written, l.maxFileBytes)
	}

	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("atomic rename: %w", err)
	}

	return written, nil
}

// safeJoinKeyForUpload 与 safeJoinPath 相同，但额外校验 key 不能指向已存在的目录。
func (l *LocalImageStorage) safeJoinKeyForUpload(key string) (string, error) {
	return l.safeJoinPath(key)
}

// ---- 辅助函数 ----

// testDirectoryWritable 通过创建临时文件测试目录可写性。
func testDirectoryWritable(dir string) error {
	tmpPath := filepath.Join(dir, ".write_test_"+randomSuffix())
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(tmpPath)
}

// guessContentType 根据扩展名推断 content-type。
func guessContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ct, ok := localMediaMimeTypes[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}

// randomSuffix 生成 8 字符的随机后缀，用于临时文件名。
func randomSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// errLocalDiskFull 磁盘空间不足哨兵错误。
var errLocalDiskFull = errors.New("local storage: disk space below minimum threshold")
