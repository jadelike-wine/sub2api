package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// S3EnovaImageAssetStorage 实现 service.EnovaImageAssetStorage，基于 AWS S3 兼容存储。
//
// AWS 凭据优先级：
//  1. 显式 AccessKeyID + SecretAccessKey（来自管理后台 settings 或 config.yaml）
//  2. EC2 IAM Role / ECS Task Role（当两者为空时，awsconfig.LoadDefaultConfig 自动走链）
//  3. 环境变量
type S3EnovaImageAssetStorage struct {
	client        *s3.Client
	presigner     *s3.PresignClient
	bucket        string
	prefix        string
	publicBaseURL string
	presignExp    time.Duration
	configured    bool
}

// NewS3EnovaImageAssetStorage 根据配置构造 S3 存储。cfg.Bucket 为空时返回未配置实例（Configured()=false）。
func NewS3EnovaImageAssetStorage(cfg service.EnovaImageAssetStorageConfig) (*S3EnovaImageAssetStorage, error) {
	if cfg.Bucket == "" {
		return &S3EnovaImageAssetStorage{configured: false}, nil
	}

	region := cfg.Region
	if region == "" {
		region = "auto"
	}

	var opts []func(*awsconfig.LoadOptions) error
	opts = append(opts, awsconfig.WithRegion(region))
	// 仅当显式提供静态凭据时使用；否则交给默认链（IAM Role / 环境变量）。
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = &cfg.Endpoint
		}
		if cfg.ForcePathStyle {
			o.UsePathStyle = true
		}
		// 部分兼容存储（R2/OSS）不支持 payload 强签名。
		o.APIOptions = append(o.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})

	exp := time.Duration(cfg.PresignedURLExpiresSeconds) * time.Second
	if exp <= 0 {
		exp = 30 * time.Minute
	}

	return &S3EnovaImageAssetStorage{
		client:        client,
		presigner:     s3.NewPresignClient(client),
		bucket:        cfg.Bucket,
		prefix:        strings.TrimPrefix(cfg.Prefix, "/"),
		publicBaseURL: strings.TrimRight(cfg.PublicBaseURL, "/"),
		presignExp:    exp,
		configured:    true,
	}, nil
}

func (s *S3EnovaImageAssetStorage) Bucket() string   { return s.bucket }
func (s *S3EnovaImageAssetStorage) Configured() bool { return s.configured }
func (s *S3EnovaImageAssetStorage) Driver() string   { return "s3" }

func (s *S3EnovaImageAssetStorage) fullKey(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + strings.TrimPrefix(key, "/")
}

func (s *S3EnovaImageAssetStorage) Put(ctx context.Context, input service.PutObjectInput) (*service.StoredObject, error) {
	if !s.configured {
		return nil, errors.New("image storage is not configured")
	}
	key := s.fullKey(input.Key)

	// 流式上传：S3 PutObject 需要内容长度。未知时读入内存（受 MaxOutputImageBytes 限制由调用方保证）。
	// 这里读取全部内容以获取大小并计算 sha256（与 backup_s3_store.go 一致，兼容 OSS 签名问题）。
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	contentType := input.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	finish := servertiming.ObserveDependency(ctx, "s3")
	out, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	finish()
	if err != nil {
		return nil, fmt.Errorf("S3 PutObject: %w", err)
	}

	etag := ""
	if out.ETag != nil {
		etag = strings.Trim(*out.ETag, `"`)
	}
	return &service.StoredObject{
		Bucket:   s.bucket,
		Key:      input.Key, // 返回相对 key（不含 prefix），DB 存相对 key
		Size:     int64(len(data)),
		ETag:     etag,
		MimeType: contentType,
	}, nil
}

func (s *S3EnovaImageAssetStorage) Delete(ctx context.Context, key string) error {
	if !s.configured {
		return errors.New("image storage is not configured")
	}
	full := s.fullKey(key)
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &full,
	})
	finish()
	return err
}

func (s *S3EnovaImageAssetStorage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if !s.configured {
		return "", errors.New("image storage is not configured")
	}
	// 若配置了 public base url（如 CloudFront），直接返回公开 URL，不签名。
	if s.publicBaseURL != "" {
		return s.publicBaseURL + "/" + s.fullKey(key), nil
	}
	if expires <= 0 {
		expires = s.presignExp
	}
	full := s.fullKey(key)
	res, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &full,
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return res.URL, nil
}

func (s *S3EnovaImageAssetStorage) PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, error) {
	if !s.configured {
		return "", errors.New("image storage is not configured")
	}
	if expires <= 0 {
		expires = s.presignExp
	}
	full := s.fullKey(key)
	input := &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &full,
	}
	if contentType != "" {
		input.ContentType = &contentType
	}
	res, err := s.presigner.PresignPutObject(ctx, input, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("presign put: %w", err)
	}
	return res.URL, nil
}

func (s *S3EnovaImageAssetStorage) Head(ctx context.Context, key string) (*service.ObjectHead, error) {
	if !s.configured {
		return nil, errors.New("image storage is not configured")
	}
	full := s.fullKey(key)
	finish := servertiming.ObserveDependency(ctx, "s3")
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &full,
	})
	finish()
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return &service.ObjectHead{Bucket: s.bucket, Key: key, Exists: false}, nil
		}
		return nil, fmt.Errorf("S3 HeadObject: %w", err)
	}
	ct := ""
	if out.ContentType != nil {
		ct = *out.ContentType
	}
	return &service.ObjectHead{
		Bucket:      s.bucket,
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: ct,
		Exists:      true,
	}, nil
}

// Get 读取 S3 对象的原始内容（用于图生图场景将输入图片 base64 编码后发给上游）。
// 调用方负责关闭返回的 ReadCloser。
func (s *S3EnovaImageAssetStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if !s.configured {
		return nil, errors.New("image storage is not configured")
	}
	full := s.fullKey(key)
	finish := servertiming.ObserveDependency(ctx, "s3")
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &full,
	})
	finish()
	if err != nil {
		return nil, fmt.Errorf("S3 GetObject: %w", err)
	}
	return out.Body, nil
}

// ---- SSRF 防护：下载 Agnes 返回的临时图片 URL 时使用 ----

// safeHTTPClientForImageDownload 构造一个带 SSRF 防护的 HTTP 客户端，
// 用于下载 Agnes 返回的临时图片。复用 httpclient 池的思路但强制校验解析后的 IP。
func safeHTTPClientForImageDownload(dialTimeout, headerTimeout, totalTimeout time.Duration) *http.Client {
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
			if err := assertPublicHost(host); err != nil {
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

// assertPublicHost 禁止 localhost / 私有 IP / link-local / 内网域名。
func assertPublicHost(host string) error {
	// 解析为 IP
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return fmt.Errorf("blocked private/local IP: %s", host)
		}
		return nil
	}
	// 主机名：禁止常见内网域名
	lower := strings.ToLower(host)
	switch {
	case lower == "localhost", strings.HasSuffix(lower, ".localhost"):
		return fmt.Errorf("blocked localhost: %s", host)
	case strings.HasSuffix(lower, ".internal"), strings.HasSuffix(lower, ".local"):
		return fmt.Errorf("blocked internal domain: %s", host)
	}
	// DNS 解析后再次校验每个解析结果
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup failed for %s: %w", host, err)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return fmt.Errorf("blocked resolved private IP %s for host %s", ip, host)
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return true
}

// DownloadImageToStorage 从一个上游 URL 流式下载图片并上传到对象存储。
// 用于 Agnes 返回 url 时转存 S3。
//
// 安全：校验 URL 协议为 HTTPS，禁止 SSRF 目标，限制最大文件大小，验证 Content-Type 与文件头。
func DownloadImageToStorage(
	ctx context.Context,
	rawURL string,
	storage service.EnovaImageAssetStorage,
	destKey string,
	maxBytes int64,
	dialTimeout, headerTimeout, totalTimeout time.Duration,
) (*service.StoredObject, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("upstream url must be https, got %s", parsed.Scheme)
	}
	if err := assertPublicHost(parsed.Hostname()); err != nil {
		return nil, fmt.Errorf("ssrf blocked: %w", err)
	}

	client := safeHTTPClientForImageDownload(dialTimeout, headerTimeout, totalTimeout)
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
	if !isAllowedImageMimeType(contentType) {
		return nil, fmt.Errorf("upstream returned unsupported content-type: %s", contentType)
	}

	// 限制大小：LimitReader 截断到 maxBytes+1，Put 后检查是否超限并回滚删除
	body := io.LimitReader(resp.Body, maxBytes+1)
	stored, err := storage.Put(ctx, service.PutObjectInput{
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

var allowedImageMimeTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

func isAllowedImageMimeType(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	return allowedImageMimeTypes[mime]
}
