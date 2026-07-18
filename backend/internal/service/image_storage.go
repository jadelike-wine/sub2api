package service

import (
	"context"
	"io"
	"time"
)

// ImageObjectStorage 是图片生成专用的对象存储抽象。
// 当前实现：AWS S3（含兼容存储 R2/OSS/MinIO）和本地磁盘存储。
//
// 安全约束：
//   - Bucket/目录默认私有，前端通过 PresignGet 获取短期访问 URL
//   - 数据库只保存对象 Key，不保存短时 Presigned URL
//   - Key 必须位于当前用户目录（media/images/{user_id}/...）
type ImageObjectStorage interface {
	// Put 上传一个对象，返回存储结果（含实际大小）。
	Put(ctx context.Context, input PutObjectInput) (*StoredObject, error)
	// Delete 删除一个对象。删除失败不能导致数据库不可恢复，调用方需记录待清理状态。
	Delete(ctx context.Context, key string) error
	// PresignGet 生成短期下载 URL（建议 10～30 分钟）。
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, error)
	// PresignPut 生成短期上传 URL（用户直传输入图片）。
	PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, error)
	// Get 读取对象的原始内容（用于图生图场景将输入图片 base64 编码后发给上游）。
	// 调用方负责关闭返回的 ReadCloser。
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Head 校验对象是否存在及元数据。
	Head(ctx context.Context, key string) (*ObjectHead, error)
	// Bucket 返回当前 bucket 名或存储标识（用于 ImageAsset.s3_bucket）。
	Bucket() string
	// Configured 报告存储是否已正确配置且可用。
	Configured() bool
	// Driver 返回存储驱动名称（"s3" 或 "local"），用于管理后台展示和路由判断。
	Driver() string
}

// PutObjectInput 描述一次上传。
type PutObjectInput struct {
	Key         string
	Body        io.Reader
	ContentType string
	// ContentLength 可选；-1 表示未知（流式）。
	ContentLength int64
}

// StoredObject 是上传结果。
type StoredObject struct {
	Bucket    string
	Key       string
	Size      int64
	ETag      string
	MimeType  string
}

// ObjectHead 是对象元数据查询结果。
type ObjectHead struct {
	Bucket      string
	Key         string
	Size        int64
	ContentType string
	Exists      bool
}

// ImageStorageConfig 是 S3 对象存储配置（可来自 config.yaml 环境变量或管理后台 settings 表覆盖）。
type ImageStorageConfig struct {
	Region          string
	Bucket          string
	Prefix          string
	Endpoint        string
	ForcePathStyle  bool
	AccessKeyID     string
	SecretAccessKey string
	PublicBaseURL   string
	// PresignedURLExpiresSeconds 预签名 URL 默认有效期（秒）。
	PresignedURLExpiresSeconds int
}

// LocalStorageConfig 是本地磁盘存储配置。
type LocalStorageConfig struct {
	// RootPath 存储根目录（如 /app/data/media）
	RootPath string
	// URLPrefix 访问 URL 前缀（如 /api/media）
	URLPrefix string
	// URLSigningSecret HMAC-SHA256 签名密钥
	URLSigningSecret string
	// PresignedURLExpiresSeconds 签名 URL 默认有效期（秒）
	PresignedURLExpiresSeconds int
	// MinFreeSpaceMB 磁盘剩余空间下限（MB）
	MinFreeSpaceMB int
	// MaxFileSizeMB 单个文件大小上限（MB）
	MaxFileSizeMB int
}
