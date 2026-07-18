package repository

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// imageGenerationPresignExpires 是 AI 生图 presigned URL 默认有效期（30 分钟）。
// 与 OpenAI/Agnes 客户端默认超时兼容。
const imageGenerationPresignExpires = 1800

// lazyImageGenerationStorage 是一个懒加载的 EnovaImageAssetStorage 实现：
// 每次操作前从数据库读取公共对象存储配置（SharedObjectStorageConfigReader），
// 当配置签名变化时重建底层 S3 客户端；配置未提供时返回 service.ErrStorageNotConfigured。
//
// 对象 key 前缀固定为 service.ImageGenerationPrefix，与 Agnes 聊天图片（agnes-chat/）
// 和数据库备份（backups/）隔离。PublicBaseURL 与备份共用同一配置。
//
// 适用于 AI 生图资产存储（输入图、输出图、签名 URL）。
type lazyImageGenerationStorage struct {
	reader  service.SharedObjectStorageConfigReader
	factory service.EnovaImageAssetStorageFactory

	mu     sync.Mutex
	cached service.EnovaImageAssetStorage
	sig    string
}

// ProvideImageGenerationStorage 构造 AI 生图专用的懒加载对象存储实例。
//
// 与数据库备份和 Agnes Chat 共用同一套 S3/R2 配置（来自 settings 表 backup_s3_config），
// 但使用独立前缀（image-generation/）隔离对象。
//
// 配置未提供时返回一个 Configured()=false 的实例（不阻塞启动），
// AI 生图服务在运行时会据此返回明确错误。
func ProvideImageGenerationStorage(
	reader service.SharedObjectStorageConfigReader,
	factory service.EnovaImageAssetStorageFactory,
) (service.EnovaImageAssetStorage, error) {
	if reader == nil {
		log.Printf("[ImageGenerationStorage] config reader is nil; storage disabled")
		return &lazyImageGenerationStorage{reader: nil, factory: factory}, nil
	}
	return &lazyImageGenerationStorage{reader: reader, factory: factory}, nil
}

// getOrCreate 从数据库读取配置并返回就绪的底层存储。
// 配置签名变化时重建 S3 客户端；未配置时返回 nil + service.ErrStorageNotConfigured。
func (s *lazyImageGenerationStorage) getOrCreate(ctx context.Context) (service.EnovaImageAssetStorage, error) {
	if s.reader == nil || s.factory == nil {
		return nil, service.ErrStorageNotConfigured
	}

	cfg, err := s.reader.GetSharedObjectStorageConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read shared object storage config: %w", err)
	}
	if cfg == nil || !cfg.IsConfigured() {
		return nil, service.ErrStorageNotConfigured
	}

	sig := configSignature(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cached != nil && s.sig == sig {
		return s.cached, nil
	}

	storageCfg := service.EnovaImageAssetStorageConfig{
		Region:                     cfg.Region,
		Bucket:                     cfg.Bucket,
		Prefix:                     service.ImageGenerationPrefix,
		Endpoint:                   cfg.Endpoint,
		ForcePathStyle:             cfg.ForcePathStyle,
		AccessKeyID:                cfg.AccessKeyID,
		SecretAccessKey:            cfg.SecretAccessKey,
		PublicBaseURL:              cfg.PublicBaseURL,
		PresignedURLExpiresSeconds: imageGenerationPresignExpires,
	}
	storage, err := s.factory(storageCfg)
	if err != nil {
		return nil, fmt.Errorf("init image generation storage: %w", err)
	}

	s.cached = storage
	s.sig = sig
	log.Printf("[ImageGenerationStorage] storage initialized/rebuilt: bucket=%s prefix=%s public_base_url_set=%v",
		cfg.Bucket, service.ImageGenerationPrefix, cfg.PublicBaseURL != "")
	return storage, nil
}

// Configured 报告存储是否已正确配置且可用。
// 由于是懒加载，此处通过尝试读取配置来判断；不构造 S3 客户端。
func (s *lazyImageGenerationStorage) Configured() bool {
	if s.reader == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cfg, err := s.reader.GetSharedObjectStorageConfig(ctx)
	if err != nil || cfg == nil {
		return false
	}
	return cfg.IsConfigured()
}

func (s *lazyImageGenerationStorage) Bucket() string { return service.ImageGenerationPrefix }
func (s *lazyImageGenerationStorage) Driver() string { return "s3" }

func (s *lazyImageGenerationStorage) Put(ctx context.Context, input service.PutObjectInput) (*service.StoredObject, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	return storage.Put(ctx, input)
}

func (s *lazyImageGenerationStorage) Delete(ctx context.Context, key string) error {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return err
	}
	return storage.Delete(ctx, key)
}

func (s *lazyImageGenerationStorage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return "", err
	}
	return storage.PresignGet(ctx, key, expires)
}

func (s *lazyImageGenerationStorage) PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return "", err
	}
	return storage.PresignPut(ctx, key, contentType, expires)
}

func (s *lazyImageGenerationStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	return storage.Get(ctx, key)
}

func (s *lazyImageGenerationStorage) Head(ctx context.Context, key string) (*service.ObjectHead, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	return storage.Head(ctx, key)
}
