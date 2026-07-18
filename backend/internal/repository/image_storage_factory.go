package repository

import (
	"fmt"
	"log"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ProvideEnovaImageAssetStorage 是对象存储工厂，根据 config.ImageGeneration.StorageDriver 选择实现。
//
// 选择逻辑：
//  1. storage_driver=local → LocalEnovaImageAssetStorage（从 config.yaml 的 local_* 字段读取配置）
//  2. storage_driver=s3（或留空）→ lazyImageGenerationStorage（懒加载，从数据库 settings 表读取共享 S3/R2 配置）
//
// S3 模式下不再从 config.yaml 的 s3_* 字段读取凭证，统一复用 backup_s3_config，
// 与数据库备份和 Agnes Chat 共用同一套配置，使用独立前缀 image-generation/ 隔离对象。
//
// 返回 service.EnovaImageAssetStorage 接口，无需 wire.Bind。
func ProvideEnovaImageAssetStorage(
	cfg *config.Config,
	reader service.SharedObjectStorageConfigReader,
	factory service.EnovaImageAssetStorageFactory,
) (service.EnovaImageAssetStorage, error) {
	ig := cfg.ImageGeneration
	driver := ig.StorageDriver

	// 留空时默认 s3（懒加载共享配置），未配置时 Configured()=false 不阻塞启动
	if driver == "" {
		driver = "s3"
		log.Printf("[EnovaImageAssetStorage] storage_driver not set, defaulting to s3 (shared config)")
	}

	switch driver {
	case "local":
		localCfg := service.EnovaLocalImageAssetStorageConfig{
			RootPath:                   ig.LocalStoragePath,
			URLPrefix:                  ig.LocalURLPrefix,
			URLSigningSecret:           ig.LocalURLSigningSecret,
			PresignedURLExpiresSeconds: ig.PresignedURLExpires,
			MinFreeSpaceMB:             ig.LocalMinFreeSpaceMB,
			MaxFileSizeMB:              ig.LocalMaxFileSizeMB,
		}
		storage, err := NewLocalEnovaImageAssetStorage(localCfg)
		if err != nil {
			log.Printf("[EnovaImageAssetStorage] WARNING: local storage init failed: %v (storage will be unavailable)", err)
			// 返回未配置实例，不阻塞启动
			return &LocalEnovaImageAssetStorage{configured: false}, nil
		}
		if !storage.Configured() {
			log.Printf("[EnovaImageAssetStorage] local storage not configured (root_path or signing_secret missing)")
		} else {
			log.Printf("[EnovaImageAssetStorage] local storage initialized at %s", ig.LocalStoragePath)
		}
		return storage, nil

	case "s3":
		// 懒加载：每次操作前从数据库读取共享 S3/R2 配置
		return ProvideImageGenerationStorage(reader, factory)

	default:
		return nil, fmt.Errorf("unknown storage_driver: %s (must be 'local' or 's3')", driver)
	}
}
