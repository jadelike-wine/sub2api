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

// agnesChatPresignExpires 是 Agnes 图片 presigned URL 默认有效期（30 分钟）。
const agnesChatPresignExpires = 1800

// NewS3EnovaImageAssetStorageFactory 返回 service.EnovaImageAssetStorageFactory，
// 包装 NewS3EnovaImageAssetStorage 供 service 层（如 Agnes 懒加载存储）使用。
// 由 wire 注入，避免 service → repository 的循环依赖。
func NewS3EnovaImageAssetStorageFactory() service.EnovaImageAssetStorageFactory {
	return func(cfg service.EnovaImageAssetStorageConfig) (service.EnovaImageAssetStorage, error) {
		return NewS3EnovaImageAssetStorage(cfg)
	}
}

// lazyAgnesChatImageStorage 是一个懒加载的 EnovaImageAssetStorage 实现：
// 每次操作前从数据库读取公共对象存储配置（SharedObjectStorageConfigReader），
// 当配置签名变化时重建底层 S3 客户端；配置未提供时返回 service.ErrStorageNotConfigured。
//
// 对象 key 前缀固定为 service.AgnesChatPrefix，与数据库备份（Prefix 字段）隔离。
type lazyAgnesChatImageStorage struct {
	reader  service.SharedObjectStorageConfigReader
	factory service.EnovaImageAssetStorageFactory

	mu     sync.Mutex
	cached service.EnovaImageAssetStorage
	sig    string
}

// ProvideAgnesChatImageStorage 构造 Agnes 多模态聊天图片上传专用的懒加载对象存储实例。
//
// 与数据库备份共用同一套 S3/R2 配置（来自 settings 表 backup_s3_config），
// 但使用独立前缀（agnes-chat/）隔离对象。
//
// 配置未提供时返回一个 Configured()=false 的实例（不阻塞启动），
// 适配器在运行时会因此对 data URL 请求返回 503。
func ProvideAgnesChatImageStorage(
	reader service.SharedObjectStorageConfigReader,
	factory service.EnovaImageAssetStorageFactory,
) (service.EnovaImageAssetStorage, error) {
	if reader == nil {
		log.Printf("[AgnesChatImageStorage] config reader is nil; adapter disabled")
		return &lazyAgnesChatImageStorage{reader: nil, factory: factory}, nil
	}
	return &lazyAgnesChatImageStorage{reader: reader, factory: factory}, nil
}

// configSignature 计算配置签名用于缓存失效判断。
// 仅包含影响 S3 客户端构造的字段，不含 Prefix（Prefix 固定为 agnes-chat）。
func configSignature(cfg *service.BackupS3Config) string {
	if cfg == nil {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s|%v|%s",
		cfg.Endpoint, cfg.Region, cfg.Bucket,
		cfg.AccessKeyID, cfg.SecretAccessKey,
		cfg.ForcePathStyle, cfg.PublicBaseURL,
	)
}

// getOrCreate 从数据库读取配置并返回就绪的底层存储。
// 配置签名变化时重建 S3 客户端；未配置时返回 nil + service.ErrStorageNotConfigured。
func (s *lazyAgnesChatImageStorage) getOrCreate(ctx context.Context) (service.EnovaImageAssetStorage, error) {
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
		Prefix:                     service.AgnesChatPrefix,
		Endpoint:                   cfg.Endpoint,
		ForcePathStyle:             cfg.ForcePathStyle,
		AccessKeyID:                cfg.AccessKeyID,
		SecretAccessKey:            cfg.SecretAccessKey,
		PublicBaseURL:              cfg.PublicBaseURL,
		PresignedURLExpiresSeconds: agnesChatPresignExpires,
	}
	storage, err := s.factory(storageCfg)
	if err != nil {
		return nil, fmt.Errorf("init agnes chat storage: %w", err)
	}

	s.cached = storage
	s.sig = sig
	log.Printf("[AgnesChatImageStorage] storage initialized/rebuilt: bucket=%s prefix=%s public_base_url_set=%v",
		cfg.Bucket, service.AgnesChatPrefix, cfg.PublicBaseURL != "")
	return storage, nil
}

// Configured 报告存储是否已正确配置且可用。
// 由于是懒加载，此处通过尝试读取配置来判断；不构造 S3 客户端。
func (s *lazyAgnesChatImageStorage) Configured() bool {
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

func (s *lazyAgnesChatImageStorage) Bucket() string { return service.AgnesChatPrefix }
func (s *lazyAgnesChatImageStorage) Driver() string { return "s3" }

func (s *lazyAgnesChatImageStorage) Put(ctx context.Context, input service.PutObjectInput) (*service.StoredObject, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	return storage.Put(ctx, input)
}

func (s *lazyAgnesChatImageStorage) Delete(ctx context.Context, key string) error {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return err
	}
	return storage.Delete(ctx, key)
}

func (s *lazyAgnesChatImageStorage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return "", err
	}
	return storage.PresignGet(ctx, key, expires)
}

func (s *lazyAgnesChatImageStorage) PresignPut(ctx context.Context, key string, contentType string, expires time.Duration) (string, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return "", err
	}
	return storage.PresignPut(ctx, key, contentType, expires)
}

func (s *lazyAgnesChatImageStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	return storage.Get(ctx, key)
}

func (s *lazyAgnesChatImageStorage) Head(ctx context.Context, key string) (*service.ObjectHead, error) {
	storage, err := s.getOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	return storage.Head(ctx, key)
}
