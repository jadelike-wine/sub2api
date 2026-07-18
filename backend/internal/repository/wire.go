package repository

import (
	"database/sql"
	"errors"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

// ProvideConcurrencyCache 创建并发控制缓存，从配置读取 TTL 参数
// 性能优化：TTL 可配置，支持长时间运行的 LLM 请求场景
func ProvideConcurrencyCache(rdb *redis.Client, cfg *config.Config) service.ConcurrencyCache {
	waitTTLSeconds := int(cfg.Gateway.Scheduling.StickySessionWaitTimeout.Seconds())
	if cfg.Gateway.Scheduling.FallbackWaitTimeout > cfg.Gateway.Scheduling.StickySessionWaitTimeout {
		waitTTLSeconds = int(cfg.Gateway.Scheduling.FallbackWaitTimeout.Seconds())
	}
	if waitTTLSeconds <= 0 {
		waitTTLSeconds = cfg.Gateway.ConcurrencySlotTTLMinutes * 60
	}
	return NewConcurrencyCache(rdb, cfg.Gateway.ConcurrencySlotTTLMinutes, waitTTLSeconds)
}

// ProvideGitHubReleaseClient 创建 GitHub Release 客户端
// 从配置中读取代理设置，支持国内服务器通过代理访问 GitHub
func ProvideGitHubReleaseClient(cfg *config.Config) service.GitHubReleaseClient {
	return NewGitHubReleaseClient(cfg.Update.ProxyURL, cfg.Security.ProxyFallback.AllowDirectOnError)
}

// ProvidePricingRemoteClient 创建定价数据远程客户端
// 从配置中读取代理设置，支持国内服务器通过代理访问 GitHub 上的定价数据
func ProvidePricingRemoteClient(cfg *config.Config) service.PricingRemoteClient {
	return NewPricingRemoteClient(cfg.Update.ProxyURL, cfg.Security.ProxyFallback.AllowDirectOnError)
}

// ProvideSessionLimitCache 创建会话限制缓存
// 用于 Anthropic OAuth/SetupToken 账号的并发会话数量控制
func ProvideSessionLimitCache(rdb *redis.Client, cfg *config.Config) service.SessionLimitCache {
	defaultIdleTimeoutMinutes := 5 // 默认 5 分钟空闲超时
	if cfg != nil && cfg.Gateway.SessionIdleTimeoutMinutes > 0 {
		defaultIdleTimeoutMinutes = cfg.Gateway.SessionIdleTimeoutMinutes
	}
	return NewSessionLimitCache(rdb, defaultIdleTimeoutMinutes)
}

// ProvideSchedulerCache 创建调度快照缓存，并注入快照分块参数。
func ProvideSchedulerCache(rdb *redis.Client, cfg *config.Config) service.SchedulerCache {
	mgetChunkSize := defaultSchedulerSnapshotMGetChunkSize
	writeChunkSize := defaultSchedulerSnapshotWriteChunkSize
	if cfg != nil {
		if cfg.Gateway.Scheduling.SnapshotMGetChunkSize > 0 {
			mgetChunkSize = cfg.Gateway.Scheduling.SnapshotMGetChunkSize
		}
		if cfg.Gateway.Scheduling.SnapshotWriteChunkSize > 0 {
			writeChunkSize = cfg.Gateway.Scheduling.SnapshotWriteChunkSize
		}
	}
	return newSchedulerCacheWithChunkSizes(rdb, mgetChunkSize, writeChunkSize)
}

// ProviderSet is the Wire provider set for all repositories
var ProviderSet = wire.NewSet(
	NewUserRepository,
	NewAPIKeyRepository,
	NewGroupRepository,
	NewAccountRepository,
	NewAdminAccountRepository,
	NewScheduledTestPlanRepository,   // 定时测试计划仓储
	NewScheduledTestResultRepository, // 定时测试结果仓储
	NewProxyRepository,
	NewRedeemCodeRepository,
	NewPromoCodeRepository,
	NewAnnouncementRepository,
	NewAnnouncementReadRepository,
	NewUsageLogRepository,
	NewUsageBillingRepository,
	NewBatchImageRepository,
	NewIdempotencyRepository,
	NewUsageCleanupRepository,
	NewDashboardAggregationRepository,
	NewSettingRepository,
	NewOpsRepository,
	NewUserSubscriptionRepository,
	NewUserAttributeDefinitionRepository,
	NewUserAttributeValueRepository,
	NewUserGroupRateRepository,
	NewErrorPassthroughRepository,
	NewTLSFingerprintProfileRepository,
	NewChannelRepository,
	NewChannelMonitorRepository,
	NewChannelMonitorRequestTemplateRepository,
	NewContentModerationRepository,
	NewAffiliateRepository,
	NewUserPlatformQuotaRepository,     // T14: user × platform quota
	NewUserPlatformQuotaServiceAdapter, // T14: adapter → service.UserPlatformQuotaRepository

	// AI 图片生成相关 repositories
	NewImageConversationRepository,
	NewImageGenerationRepository,
	NewImageAssetRepository,
	NewImageCredentialRepository,
	ProvideS3EnovaImageAssetStorage,
	// ProvideEnovaImageAssetStorage 是存储工厂，根据 storage_driver 选择 local 或 s3 实现。
	// 返回 service.EnovaImageAssetStorage 接口，替代之前的 wire.Bind 方式。
	ProvideEnovaImageAssetStorage,

	// Cache implementations
	NewGatewayCache,
	NewBillingCache,
	NewAPIKeyCache,
	NewTempUnschedCache,
	NewTimeoutCounterCache,
	NewOpenAI403CounterCache,
	NewInternal500CounterCache,
	ProvideConcurrencyCache,
	ProvideSessionLimitCache,
	NewRPMCache,
	NewUserRPMCache,
	NewUserMsgQueueCache,
	NewDashboardCache,
	NewEmailCache,
	NewIdentityCache,
	NewRedeemCache,
	NewUpdateCache,
	NewGeminiTokenCache,
	NewBatchImageQueue,
	NewBatchImageDownloadLimiter,
	NewLeaderLockCache,
	ProvideSchedulerCache,
	NewSchedulerOutboxRepository,
	NewProxyLatencyCache,
	NewTotpCache,
	NewRefreshTokenCache,
	NewErrorPassthroughCache,
	NewTLSFingerprintProfileCache,
	NewContentModerationHashCache,

	// Encryptors
	NewAESEncryptor,

	// Backup infrastructure
	NewPgDumper,
	NewS3BackupStoreFactory,

	// HTTP service ports (DI Strategy A: return interface directly)
	NewTurnstileVerifier,
	ProvidePricingRemoteClient,
	ProvideGitHubReleaseClient,
	NewProxyExitInfoProber,
	NewClaudeUsageFetcher,
	NewClaudeOAuthClient,
	NewHTTPUpstream,
	NewOpenAIOAuthClient,
	NewGrokOAuthClient,
	NewGeminiOAuthClient,
	NewGeminiCliCodeAssistClient,
	NewGeminiDriveClient,

	ProvideEnt,
	ProvideSQLDB,
	ProvideRedis,
)

// ProvideEnt 为依赖注入提供 Ent 客户端。
//
// 该函数是 InitEnt 的包装器，符合 Wire 的依赖提供函数签名要求。
// Wire 会在编译时分析依赖关系，自动生成初始化代码。
//
// 依赖：config.Config
// 提供：*ent.Client
func ProvideEnt(cfg *config.Config) (*ent.Client, error) {
	client, _, err := InitEnt(cfg)
	return client, err
}

// ProvideSQLDB 从 Ent 客户端提取底层的 *sql.DB 连接。
//
// 某些 Repository 需要直接执行原生 SQL（如复杂的批量更新、聚合查询），
// 此时需要访问底层的 sql.DB 而不是通过 Ent ORM。
//
// 设计说明：
//   - Ent 底层使用 sql.DB，通过 Driver 接口可以访问
//   - 这种设计允许在同一事务中混用 Ent 和原生 SQL
//
// 依赖：*ent.Client
// 提供：*sql.DB
func ProvideSQLDB(client *ent.Client) (*sql.DB, error) {
	if client == nil {
		return nil, errors.New("nil ent client")
	}
	// 从 Ent 客户端获取底层驱动
	drv, ok := client.Driver().(*entsql.Driver)
	if !ok {
		return nil, errors.New("ent driver does not expose *sql.DB")
	}
	// 返回驱动持有的 sql.DB 实例
	return drv.DB(), nil
}

// ProvideRedis 为依赖注入提供 Redis 客户端。
//
// Redis 用于：
//   - 分布式锁（如并发控制）
//   - 缓存（如用户会话、API 响应缓存）
//   - 速率限制
//   - 实时统计数据
//
// 依赖：config.Config
// 提供：*redis.Client
func ProvideRedis(cfg *config.Config) *redis.Client {
	return InitRedis(cfg)
}

// ProvideS3EnovaImageAssetStorage 从 config.Config 构造 S3 图片存储。
//
// AWS 凭据优先级：
//  1. config.yaml / 环境变量中的静态 AccessKeyID + SecretAccessKey
//  2. EC2 IAM Role / ECS Task Role（当两者为空时，awsconfig.LoadDefaultConfig 自动走链）
//
// Bucket 为空时返回未配置实例（Configured()=false），不阻塞启动。
//
// 依赖：*config.Config
// 提供：*S3EnovaImageAssetStorage（由 service 层通过 wire.Bind 绑定到 service.EnovaImageAssetStorage）
func ProvideS3EnovaImageAssetStorage(cfg *config.Config) (*S3EnovaImageAssetStorage, error) {
	ig := cfg.ImageGeneration
	storageCfg := service.EnovaImageAssetStorageConfig{
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
	return NewS3EnovaImageAssetStorage(storageCfg)
}
