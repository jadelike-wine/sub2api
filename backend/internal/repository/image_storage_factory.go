package repository

import (
	"fmt"
	"log"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ProvideImageStorage 是对象存储工厂，根据 config.ImageGeneration.StorageDriver 选择实现。
//
// 选择逻辑：
//  1. storage_driver=local → LocalImageStorage（不要求任何 S3 配置）
//  2. storage_driver=s3 → S3ImageStorage（原有逻辑）
//  3. storage_driver 留空 → 自动推断：有 S3Bucket 则用 s3，否则用 local
//
// 返回 service.ImageObjectStorage 接口，无需 wire.Bind。
func ProvideImageStorage(cfg *config.Config) (service.ImageObjectStorage, error) {
	ig := cfg.ImageGeneration
	driver := ig.StorageDriver

	// 自动推断
	if driver == "" {
		if ig.S3Bucket != "" {
			driver = "s3"
		} else {
			driver = "local"
		}
		log.Printf("[ImageStorage] storage_driver not set, auto-detected: %s", driver)
	}

	switch driver {
	case "local":
		localCfg := service.LocalStorageConfig{
			RootPath:                   ig.LocalStoragePath,
			URLPrefix:                  ig.LocalURLPrefix,
			URLSigningSecret:           ig.LocalURLSigningSecret,
			PresignedURLExpiresSeconds: ig.PresignedURLExpires,
			MinFreeSpaceMB:             ig.LocalMinFreeSpaceMB,
			MaxFileSizeMB:              ig.LocalMaxFileSizeMB,
		}
		storage, err := NewLocalImageStorage(localCfg)
		if err != nil {
			log.Printf("[ImageStorage] WARNING: local storage init failed: %v (storage will be unavailable)", err)
			// 返回未配置实例，不阻塞启动
			return &LocalImageStorage{configured: false}, nil
		}
		if !storage.Configured() {
			log.Printf("[ImageStorage] local storage not configured (root_path or signing_secret missing)")
		} else {
			log.Printf("[ImageStorage] local storage initialized at %s", ig.LocalStoragePath)
		}
		return storage, nil

	case "s3":
		s3Cfg := service.ImageStorageConfig{
			Region:                     ig.S3Region,
			Bucket:                     ig.S3Bucket,
			Prefix:                     ig.S3Prefix,
			Endpoint:                   ig.S3Endpoint,
			ForcePathStyle:             ig.S3ForcePathStyle,
			AccessKeyID:                ig.S3AccessKeyID,
			SecretAccessKey:            ig.S3SecretAccessKey,
			PublicBaseURL:              ig.S3PublicBaseURL,
			PresignedURLExpiresSeconds: ig.PresignedURLExpires,
		}
		storage, err := NewS3ImageStorage(s3Cfg)
		if err != nil {
			return nil, fmt.Errorf("init s3 storage: %w", err)
		}
		if !storage.Configured() {
			log.Printf("[ImageStorage] S3 storage not configured (bucket empty)")
		}
		return storage, nil

	default:
		return nil, fmt.Errorf("unknown storage_driver: %s (must be 'local' or 's3')", driver)
	}
}
