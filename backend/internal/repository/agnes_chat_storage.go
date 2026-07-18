package repository

import (
	"fmt"
	"log"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ProvideAgnesChatImageStorage 构造 Agnes 多模态聊天图片上传专用的对象存储实例。
//
// 与生图资产桶隔离：使用独立的 R2/S3 bucket、prefix、生命周期策略，
// 避免污染生图资产。Agnes 必须能从公网访问返回的 URL。
//
// 未配置（R2.Bucket 为空）时返回未配置实例（Configured()=false），
// 适配器在运行时会因此对 data URL 请求返回 503，避免启动阻塞。
func ProvideAgnesChatImageStorage(cfg *config.Config) (service.EnovaImageAssetStorage, error) {
	r2 := cfg.AgnesChat.R2

	if !r2.IsConfigured() {
		// 显式列出缺失字段，便于管理员快速定位
		missing := []string{}
		if r2.Endpoint == "" {
			missing = append(missing, "endpoint")
		}
		if r2.Bucket == "" {
			missing = append(missing, "bucket")
		}
		if r2.AccessKeyID == "" {
			missing = append(missing, "access_key_id")
		}
		if r2.SecretAccessKey == "" {
			missing = append(missing, "secret_access_key")
		}
		log.Printf("[AgnesChatImageStorage] R2 not configured (missing: %s); adapter disabled", strings.Join(missing, ","))
		// 返回未配置实例，不阻塞启动
		storage, err := NewS3EnovaImageAssetStorage(service.EnovaImageAssetStorageConfig{})
		if err != nil {
			return nil, fmt.Errorf("init agnes chat storage (unconfigured): %w", err)
		}
		return storage, nil
	}

	s3Cfg := service.EnovaImageAssetStorageConfig{
		Region:                     r2.Region,
		Bucket:                     r2.Bucket,
		Prefix:                     r2.Prefix,
		Endpoint:                   r2.Endpoint,
		ForcePathStyle:             r2.ForcePathStyle,
		AccessKeyID:                r2.AccessKeyID,
		SecretAccessKey:            r2.SecretAccessKey,
		PublicBaseURL:              r2.PublicBaseURL,
		PresignedURLExpiresSeconds: r2.PresignExpiresSeconds,
	}
	storage, err := NewS3EnovaImageAssetStorage(s3Cfg)
	if err != nil {
		return nil, fmt.Errorf("init agnes chat r2 storage: %w", err)
	}
	if !storage.Configured() {
		return nil, fmt.Errorf("agnes chat r2 storage initialized but Configured()=false (unexpected)")
	}
	log.Printf("[AgnesChatImageStorage] R2 storage initialized: bucket=%s prefix=%s public_base_url_set=%v",
		r2.Bucket, r2.Prefix, r2.PublicBaseURL != "")
	return storage, nil
}
