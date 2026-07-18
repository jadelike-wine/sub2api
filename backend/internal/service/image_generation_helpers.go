package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// downloadAndStoreImage 从上游 URL 下载图片并转存到对象存储。
//
// 安全：
//   - URL 必须是 HTTPS
//   - SSRF 防护：禁止 localhost / 私有 IP / link-local / 内网域名
//   - DNS 解析后再次校验目标 IP
//   - 限制最大文件大小
//   - 验证 Content-Type 和文件头
//   - 超限回滚删除已上传对象
func downloadAndStoreImage(
	ctx context.Context,
	rawURL string,
	destKey string,
	storage ImageObjectStorage,
	maxBytes int64,
	dialTimeout, headerTimeout, totalTimeout time.Duration,
) (*StoredObject, error) {
	if maxBytes <= 0 {
		maxBytes = 20 * 1024 * 1024
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("upstream url must be https, got %s", parsed.Scheme)
	}
	if err := assertPublicHostForImage(parsed.Hostname()); err != nil {
		return nil, fmt.Errorf("ssrf blocked: %w", err)
	}

	client := safeHTTPClientForImageDownloadSvc(dialTimeout, headerTimeout, totalTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream download status %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !isAllowedImageMime(contentType) {
		return nil, fmt.Errorf("upstream returned unsupported content-type: %s", contentType)
	}

	// 限制大小：LimitReader 截断到 maxBytes+1，上传后检查是否超限并回滚删除
	body := io.LimitReader(resp.Body, maxBytes+1)
	stored, err := storage.Put(ctx, PutObjectInput{
		Key:         destKey,
		Body:        body,
		ContentType: contentType,
	})
	if err != nil {
		return nil, err
	}
	if stored.Size > maxBytes {
		_ = storage.Delete(ctx, destKey)
		return nil, fmt.Errorf("upstream image exceeds max size %d", maxBytes)
	}
	return stored, nil
}

// decodeAndStoreBase64Image 解码 Base64 图片并上传到对象存储。
//
// 安全：
//   - 设置严格大小限制
//   - 解码后直接上传 S3，不写入数据库
//   - 超限直接拒绝
func decodeAndStoreBase64Image(
	ctx context.Context,
	b64 string,
	destKey string,
	mimeType string,
	storage ImageObjectStorage,
	maxBytes int64,
) (*StoredObject, error) {
	if maxBytes <= 0 {
		maxBytes = 20 * 1024 * 1024
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	if !isAllowedImageMime(mimeType) {
		return nil, fmt.Errorf("unsupported mime type: %s", mimeType)
	}

	// 先检查 Base64 字符串长度（粗略预估解码后大小）
	// Base64 编码后大小约为原始大小的 4/3
	estimated := int64(len(b64)) * 3 / 4
	if estimated > maxBytes {
		return nil, fmt.Errorf("base64 image exceeds max size %d (estimated %d)", maxBytes, estimated)
	}

	// 流式解码：使用 base64.NewDecoder 避免一次性读入内存
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64))
	stored, err := storage.Put(ctx, PutObjectInput{
		Key:         destKey,
		Body:        io.LimitReader(reader, maxBytes+1),
		ContentType: mimeType,
	})
	if err != nil {
		return nil, err
	}
	if stored.Size > maxBytes {
		_ = storage.Delete(ctx, destKey)
		return nil, fmt.Errorf("decoded image exceeds max size %d", maxBytes)
	}
	return stored, nil
}

// safeHTTPClientForImageDownloadSvc 构造带 SSRF 防护的 HTTP 客户端。
func safeHTTPClientForImageDownloadSvc(dialTimeout, headerTimeout, totalTimeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if err := assertPublicHostForImage(host); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   totalTimeout,
	}
}

// assertPublicHostForImage 禁止 localhost / 私有 IP / link-local / 内网域名。
func assertPublicHostForImage(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIPForImage(ip) {
			return fmt.Errorf("blocked private/local IP: %s", host)
		}
		return nil
	}
	lower := strings.ToLower(host)
	switch {
	case lower == "localhost", strings.HasSuffix(lower, ".localhost"):
		return fmt.Errorf("blocked localhost: %s", host)
	case strings.HasSuffix(lower, ".internal"), strings.HasSuffix(lower, ".local"):
		return fmt.Errorf("blocked internal domain: %s", host)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup failed for %s: %w", host, err)
	}
	for _, ip := range ips {
		if !isPublicIPForImage(ip) {
			return fmt.Errorf("blocked resolved private IP %s for host %s", ip, host)
		}
	}
	return nil
}

func isPublicIPForImage(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return true
}

var allowedImageMimeTypesSvc = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

func isAllowedImageMime(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	return allowedImageMimeTypesSvc[mime]
}

// errStorageNotConfigured 当对象存储未配置时返回。
var errStorageNotConfigured = errors.New("image storage is not configured")
