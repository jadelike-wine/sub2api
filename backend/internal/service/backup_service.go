package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

const (
	settingKeyBackupS3Config = "backup_s3_config"
	settingKeyBackupSchedule = "backup_schedule"
	settingKeyBackupRecords  = "backup_records"

	maxBackupRecords = 100

	// AgnesChatPrefix 是 Agnes 多模态聊天图片在公共对象存储中的 key 前缀。
	// 与数据库备份（Prefix 字段控制，默认 "backups"）隔离，避免互相覆盖。
	AgnesChatPrefix = "agnes-chat"

	// ImageGenerationPrefix 是 AI 生图资产在公共对象存储中的 key 前缀。
	// 与 Agnes 聊天图片（agnes-chat/）和数据库备份（backups/）隔离。
	ImageGenerationPrefix = "image-generation"

	// backupProbeTimeout 是 PublicBaseURL backups/ 公开性探测的单次 HTTP 超时。
	// 短超时避免阻塞保存操作；CDN 不可达时 fail-closed 拒绝保存。
	backupProbeTimeout = 5 * time.Second
)

var (
	ErrBackupS3NotConfigured = infraerrors.BadRequest("BACKUP_S3_NOT_CONFIGURED", "backup S3 storage is not configured")
	ErrBackupNotFound        = infraerrors.NotFound("BACKUP_NOT_FOUND", "backup record not found")
	ErrBackupInProgress      = infraerrors.Conflict("BACKUP_IN_PROGRESS", "a backup is already in progress")
	ErrRestoreInProgress     = infraerrors.Conflict("RESTORE_IN_PROGRESS", "a restore is already in progress")
	ErrBackupRecordsCorrupt  = infraerrors.InternalServer("BACKUP_RECORDS_CORRUPT", "backup records data is corrupted")
	ErrBackupS3ConfigCorrupt = infraerrors.InternalServer("BACKUP_S3_CONFIG_CORRUPT", "backup S3 config data is corrupted")

	// ErrBackupPrefixPubliclyReadable 表示 PublicBaseURL 指向的域名允许匿名读取 backups/ 前缀，
	// 数据库备份将公开暴露。UpdateS3Config 会通过探测拒绝保存此类配置（fail-closed）。
	// 部署 deploy/cloudflare-worker/r2-access-policy.js 或移除 PublicBaseURL 可解决。
	ErrBackupPrefixPubliclyReadable = infraerrors.BadRequest("BACKUP_PREFIX_PUBLICLY_READABLE", "backups/ prefix is publicly readable via PublicBaseURL")

	// ErrBucketPrivacyNotAttested 表示管理员未勾选 bucket 私有化承诺。
	// PublicBaseURL 非空时，管理员必须显式承诺：
	//   - 已禁用 R2 Public Development URL（*.r2.dev）
	//   - 已移除或 Worker-protect 所有未受保护的 custom domain
	//   - bucket 未启用任何公开读策略
	// 后端无法直接读取 Cloudflare 控制台配置；声明式 AdditionalPublicBaseURLs 也可能漏报。
	// 此承诺是 HARD 前提，真正可验证的边界由 verify-policy.mjs（Cloudflare API 权威获取）提供。
	ErrBucketPrivacyNotAttested = infraerrors.BadRequest("BUCKET_PRIVACY_NOT_ATTESTED", "bucket privacy attestation is required when PublicBaseURL is set")

	// ErrSecretEncryptionKeyNotConfigured is returned when an S3 SecretAccessKey
	// would be encrypted with an auto-generated (ephemeral) key. That key is
	// regenerated on every process start, so the persisted ciphertext becomes
	// undecryptable after a restart/upgrade ("cipher: message authentication
	// failed"), silently breaking S3 backup/image storage (#4524). Mirrors the
	// existing guards for payments (payment.ProvideEncryptionKey) and TOTP
	// enablement, which likewise refuse to depend on an auto-generated key.
	ErrSecretEncryptionKeyNotConfigured = infraerrors.BadRequest(
		"SECRET_ENCRYPTION_KEY_NOT_CONFIGURED",
		"cannot store the S3 secret access key: no fixed secret encryption key is configured, so the auto-generated key would change on every restart and make the stored secret undecryptable after a restart or upgrade. Set a fixed TOTP_ENCRYPTION_KEY (e.g. generate one with `openssl rand -hex 32`) and try again",
	)
)

// ─── 接口定义 ───

// DBDumper abstracts database dump/restore operations
type DBDumper interface {
	Dump(ctx context.Context) (io.ReadCloser, error)
	Restore(ctx context.Context, data io.Reader) error
}

// BackupObjectStore abstracts object storage for backup files
type BackupObjectStore interface {
	Upload(ctx context.Context, key string, body io.Reader, contentType string) (sizeBytes int64, err error)
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error)
	HeadBucket(ctx context.Context) error
}

// BackupObjectStoreFactory creates an object store from S3 config
type BackupObjectStoreFactory func(ctx context.Context, cfg *BackupS3Config) (BackupObjectStore, error)

// ─── 数据模型 ───

// BackupS3Config 是系统公共对象存储配置（S3 兼容，支持 Cloudflare R2）。
// 同时用于数据库备份和 Agnes 多模态图片存储。
type BackupS3Config struct {
	Endpoint        string `json:"endpoint"` // e.g. https://<account_id>.r2.cloudflarestorage.com
	Region          string `json:"region"`   // R2 用 "auto"
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key,omitempty"` //nolint:revive // field name follows AWS convention
	Prefix          string `json:"prefix"` // 数据库备份 key 前缀，如 "backups"（Agnes 图片前缀固定为 AgnesChatPrefix）
	ForcePathStyle  bool   `json:"force_path_style"`
	PublicBaseURL   string `json:"public_base_url"` // 公开桶/CDN 域名；Agnes 图片生成 HTTPS 直链时使用
	// AdditionalPublicBaseURLs 声明该 bucket 的其他公开访问入口（r2.dev Public Development URL、
	// 其他 custom domain 等）。探测会验证所有这些 URL 同样拒绝 backups/ 前缀的匿名访问。
	//
	// ⚠️ 这是 SECONDARY 纵深防御，不是主要安全边界。
	// 管理员可能漏报公开入口，后端无法发现未声明的域名。真正的强制边界是：
	//   1. BucketPrivacyAttested（HARD 前提，见下）——管理员书面承诺已私有化 bucket
	//   2. deploy/cloudflare-worker/verify-policy.mjs——通过 Cloudflare API 权威获取
	//      r2.dev 状态和 custom domain 列表后逐一探测，不依赖人工声明
	// 推荐做法：在 Cloudflare 控制台禁用 r2.dev、移除未受 Worker 保护的 custom domain，
	// 此时此字段留空即可。
	AdditionalPublicBaseURLs []string `json:"additional_public_base_urls,omitempty"`

	// BucketPrivacyAttested 是管理员的硬性运维承诺（HARD 前提）：
	//   - R2 Public Development URL（*.r2.dev）已禁用
	//   - 所有 custom domain 已移除，或已路由到受 r2-access-policy.js 保护的 Worker
	//   - bucket 未启用任何公开读策略
	//   - AdditionalPublicBaseURLs 已完整列出所有剩余公开入口（如有）
	//
	// 当 PublicBaseURL 非空时，UpdateS3Config/TestS3Connection 强制要求此字段为 true，
	// 否则拒绝保存/测试。这是声明式安全机制无法关闭 bucket 级公开风险的弥补：
	// 后端无法直接读取 Cloudflare 控制台配置，只能依赖管理员书面承诺。
	//
	// 真正的可验证边界由 deploy/cloudflare-worker/verify-policy.mjs 提供——
	// 该脚本通过 Cloudflare API 权威获取所有公开入口并逐一探测，应纳入 CI/定期巡检。
	BucketPrivacyAttested bool `json:"bucket_privacy_attested,omitempty"`
}

// IsConfigured 检查必要字段是否已配置
func (c *BackupS3Config) IsConfigured() bool {
	return c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// SharedObjectStorageConfigReader 从数据库读取公共对象存储配置（解密后）。
// 由 BackupService 实现，供 Agnes 图片存储等模块使用，避免业务代码直接查询 settings 表。
type SharedObjectStorageConfigReader interface {
	// GetSharedObjectStorageConfig 返回解密后的公共 S3/R2 配置。
	// 返回 nil, nil 表示尚未配置（调用方应据此返回明确错误，不静默回退）。
	GetSharedObjectStorageConfig(ctx context.Context) (*BackupS3Config, error)
}

// EnovaImageAssetStorageFactory 根据配置构造 EnovaImageAssetStorage。
// 由 repository 层实现（包装 NewS3EnovaImageAssetStorage），通过 wire 注入到 service 层。
type EnovaImageAssetStorageFactory func(EnovaImageAssetStorageConfig) (EnovaImageAssetStorage, error)

// BackupScheduleConfig 定时备份配置
type BackupScheduleConfig struct {
	Enabled     bool   `json:"enabled"`
	CronExpr    string `json:"cron_expr"`    // cron 表达式，如 "0 2 * * *" 每天凌晨2点
	RetainDays  int    `json:"retain_days"`  // 备份文件过期天数，默认14，0=不自动清理
	RetainCount int    `json:"retain_count"` // 最多保留份数，0=不限制
}

// BackupRecord 备份记录
type BackupRecord struct {
	ID            string `json:"id"`
	Status        string `json:"status"`      // pending, running, completed, failed
	BackupType    string `json:"backup_type"` // postgres
	FileName      string `json:"file_name"`
	S3Key         string `json:"s3_key"`
	SizeBytes     int64  `json:"size_bytes"`
	TriggeredBy   string `json:"triggered_by"` // manual, scheduled
	ErrorMsg      string `json:"error_message,omitempty"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`     // 过期时间
	Progress      string `json:"progress,omitempty"`       // "dumping", "uploading", ""
	RestoreStatus string `json:"restore_status,omitempty"` // "", "running", "completed", "failed"
	RestoreError  string `json:"restore_error,omitempty"`
	RestoredAt    string `json:"restored_at,omitempty"`
}

// httpProbeDoer 是 backup 前缀公开性探测的 HTTP 客户端抽象。
// 默认使用 http.DefaultClient；测试中可注入 mock 以验证探测逻辑（避免真实网络）。
type httpProbeDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// BackupService 数据库备份恢复服务
type BackupService struct {
	settingRepo SettingRepository
	dbCfg       *config.DatabaseConfig
	encryptor   SecretEncryptor
	// encryptionKeyConfigured mirrors cfg.Totp.EncryptionKeyConfigured: false
	// means the secret encryption key was auto-generated and does not survive a
	// restart. Durable-secret writers must refuse to persist new secrets in that
	// mode (#4524).
	encryptionKeyConfigured bool
	storeFactory            BackupObjectStoreFactory
	dumper                  DBDumper

	opMu      sync.Mutex // 保护 backingUp/restoring 标志
	backingUp bool
	restoring bool

	storeMu   sync.Mutex // 保护 store/s3Cfg/storeSig 缓存
	store     BackupObjectStore
	s3Cfg     *BackupS3Config
	storeSig  string // 配置指纹，用于多实例下检测配置变更并重建客户端

	recordsMu sync.Mutex // 保护 records 的 load/save 操作

	cronMu      sync.Mutex
	cronSched   *cron.Cron
	cronEntryID cron.EntryID

	wg           sync.WaitGroup     // 追踪活跃的备份/恢复 goroutine
	shuttingDown atomic.Bool        // 阻止新备份启动
	bgCtx        context.Context    // 所有后台操作的 parent context
	bgCancel     context.CancelFunc // 取消所有活跃后台操作

	probeDoer httpProbeDoer // PublicBaseURL 前缀公开性探测客户端（默认 http.DefaultClient）
}

func NewBackupService(
	settingRepo SettingRepository,
	cfg *config.Config,
	encryptor SecretEncryptor,
	storeFactory BackupObjectStoreFactory,
	dumper DBDumper,
) *BackupService {
	bgCtx, bgCancel := context.WithCancel(context.Background())
	return &BackupService{
		settingRepo:             settingRepo,
		dbCfg:                   &cfg.Database,
		encryptor:               encryptor,
		encryptionKeyConfigured: cfg.Totp.EncryptionKeyConfigured,
		storeFactory:            storeFactory,
		dumper:                  dumper,
		bgCtx:                   bgCtx,
		bgCancel:                bgCancel,
		probeDoer:               newBackupProbeHTTPClient(),
	}
}

// Start 启动定时备份调度器并清理孤立记录
func (s *BackupService) Start() {
	s.cronSched = cron.New()
	s.cronSched.Start()

	// 清理重启后孤立的 running 记录
	s.recoverStaleRecords()

	// 加载已有的定时配置
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	schedule, err := s.GetSchedule(ctx)
	if err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 加载定时备份配置失败: %v", err)
		return
	}
	if schedule.Enabled && schedule.CronExpr != "" {
		if err := s.applyCronSchedule(schedule); err != nil {
			logger.LegacyPrintf("service.backup", "[Backup] 应用定时备份配置失败: %v", err)
		}
	}
}

// recoverStaleRecords 启动时将孤立的 running 记录标记为 failed
func (s *BackupService) recoverStaleRecords() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	records, err := s.loadRecords(ctx)
	if err != nil {
		return
	}
	for i := range records {
		if records[i].Status == "running" {
			records[i].Status = "failed"
			records[i].ErrorMsg = "interrupted by server restart"
			records[i].Progress = ""
			records[i].FinishedAt = time.Now().Format(time.RFC3339)
			_ = s.saveRecord(ctx, &records[i])
			logger.LegacyPrintf("service.backup", "[Backup] recovered stale running record: %s", records[i].ID)
		}
		if records[i].RestoreStatus == "running" {
			records[i].RestoreStatus = "failed"
			records[i].RestoreError = "interrupted by server restart"
			_ = s.saveRecord(ctx, &records[i])
			logger.LegacyPrintf("service.backup", "[Backup] recovered stale restoring record: %s", records[i].ID)
		}
	}
}

// Stop 停止定时备份并等待活跃操作完成
func (s *BackupService) Stop() {
	s.shuttingDown.Store(true)

	s.cronMu.Lock()
	if s.cronSched != nil {
		s.cronSched.Stop()
	}
	s.cronMu.Unlock()

	// 等待活跃备份/恢复完成（最多 5 分钟）
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		logger.LegacyPrintf("service.backup", "[Backup] all active operations finished")
	case <-time.After(5 * time.Minute):
		logger.LegacyPrintf("service.backup", "[Backup] shutdown timeout after 5min, cancelling active operations")
		if s.bgCancel != nil {
			s.bgCancel() // 取消所有后台操作
		}
		// 给 goroutine 时间响应取消并完成清理
		select {
		case <-done:
			logger.LegacyPrintf("service.backup", "[Backup] active operations cancelled and cleaned up")
		case <-time.After(10 * time.Second):
			logger.LegacyPrintf("service.backup", "[Backup] goroutine cleanup timed out")
		}
	}
}

// ─── S3 配置管理 ───

// EncryptionKeyConfigured reports whether a fixed (explicitly configured) secret
// encryption key is in use. When false the key is auto-generated on every start
// and secrets encrypted with it cannot be recovered after a restart, so callers
// that persist durable secrets must refuse to do so (#4524).
func (s *BackupService) EncryptionKeyConfigured() bool {
	return s != nil && s.encryptionKeyConfigured
}

func (s *BackupService) GetS3Config(ctx context.Context) (*BackupS3Config, error) {
	cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &BackupS3Config{}, nil
	}
	// 脱敏返回
	cfg.SecretAccessKey = ""
	return cfg, nil
}

// GetSharedObjectStorageConfig 实现 SharedObjectStorageConfigReader 接口。
// 返回解密后的完整配置（含 SecretAccessKey），供 Agnes 图片存储等模块构造 S3 客户端。
// 返回 nil, nil 表示尚未配置——调用方应据此返回明确错误，不静默回退到环境变量。
func (s *BackupService) GetSharedObjectStorageConfig(ctx context.Context) (*BackupS3Config, error) {
	cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.IsConfigured() {
		return nil, nil
	}
	return cfg, nil
}

func (s *BackupService) UpdateS3Config(ctx context.Context, cfg BackupS3Config) (*BackupS3Config, error) {
	// 强制规范化备份前缀：固定为 "backups"，防止跨前缀写入或根目录写入。
	// Agnes 图片使用 agnes-chat/，AI 生图使用 image-generation/，均由常量固定，不由此配置控制。
	cfg.Prefix = normalizeBackupPrefix(cfg.Prefix)

	// 加载旧配置（用于保留原 SecretAccessKey 密文）。
	// 必须传播读取错误：若 settings 暂时故障或 JSON 损坏，old 为 nil 且 err 非 nil，
	// 此时若管理员留空 Secret 修改其他字段，会保存一个没有 Secret 的新配置，
	// 覆盖已存在的凭证，使备份/Agnes/生图三条共享存储链路同时不可用。
	old, err := s.loadS3ConfigRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("load previous s3 config: %w", err)
	}

	if cfg.SecretAccessKey == "" {
		// 编辑时留空：保留旧密文，避免解密后再以明文写回（防止明文泄露）。
		// old == nil 表示此前未配置过（首次配置分两步填写的场景），允许空 Secret 保存。
		if old != nil {
			cfg.SecretAccessKey = old.SecretAccessKey
		}
	} else {
		// 提供了新 secret：拒绝用自动生成的临时密钥加密，该密钥每次重启都会变化，
		// 落库的密文在重启/升级后无法解密（#4524）。与支付、TOTP 的处理保持一致。
		if !s.encryptionKeyConfigured {
			return nil, ErrSecretEncryptionKeyNotConfigured
		}
		// 加密 SecretAccessKey 后保存
		encrypted, err := s.encryptor.Encrypt(cfg.SecretAccessKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt secret: %w", err)
		}
		cfg.SecretAccessKey = encrypted
	}

	// PublicBaseURL 安全边界验证（fail-closed）：
	// 随机路径/日志/UI 警告都不能阻止 R2 custom domain / public bucket 的匿名读取。
	// 此处通过探测 backups/.policy-probe-<uuid> 验证 CDN/Worker 策略是否拒绝 backups/ 前缀：
	//   - 403：策略生效（Worker 已部署或 bucket 私有）→ 允许保存
	//   - 404：backups/ 公开可读 → 拒绝保存，要求先部署 deploy/cloudflare-worker/r2-access-policy.js
	//   - 网络错误：fail-closed 拒绝保存（旧 warn-only 会允许未验证的公开配置）
	//
	// 必须探测所有公开入口（PublicBaseURL + AdditionalPublicBaseURLs）：
	// Worker 仅保护 PublicBaseURL 这一路由，r2.dev Public Development URL 或未受保护的
	// custom domain 可绕过。管理员必须声明所有公开入口，后端逐一验证策略生效；
	// 或在 Cloudflare 控制台禁用这些入口（推荐，此时 AdditionalPublicBaseURLs 留空）。
	//
	// ⚠️ 声明式列表是 SECONDARY 纵深防御——管理员可能漏报。HARD 前提是 BucketPrivacyAttested
	// （管理员书面承诺 bucket 已私有化），真正可验证的边界由 verify-policy.mjs 通过
	// Cloudflare API 权威获取公开入口后逐一探测。
	if cfg.PublicBaseURL != "" && !cfg.BucketPrivacyAttested {
		return nil, ErrBucketPrivacyNotAttested
	}
	publicURLs := []string{cfg.PublicBaseURL}
	publicURLs = append(publicURLs, cfg.AdditionalPublicBaseURLs...)
	for _, u := range publicURLs {
		if u == "" {
			continue
		}
		if err := s.verifyBackupPrefixNotPublic(ctx, u); err != nil {
			return nil, fmt.Errorf("verify public url %q: %w", u, err)
		}
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal s3 config: %w", err)
	}
	if err := s.settingRepo.Set(ctx, settingKeyBackupS3Config, string(data)); err != nil {
		return nil, fmt.Errorf("save s3 config: %w", err)
	}

	// 清除缓存的 S3 客户端（本实例）
	s.storeMu.Lock()
	s.store = nil
	s.s3Cfg = nil
	s.storeSig = ""
	s.storeMu.Unlock()

	cfg.SecretAccessKey = ""
	return &cfg, nil
}

// loadS3ConfigRaw 从数据库加载原始配置（不解密 SecretAccessKey）。
// 用于 UpdateS3Config 保留原密文，避免解密→明文回写。
//
// 错误语义：
//   - settings repository 读取失败（如 DB 暂时不可用）：返回 (nil, err)，
//     调用方必须传播此错误，否则会在留空 Secret 编辑其他字段时静默覆盖既有凭证。
//   - raw == ""（尚未配置）：返回 (nil, nil)，表示首次配置场景。
//   - JSON 损坏：返回 (nil, ErrBackupS3ConfigCorrupt)。
func (s *BackupService) loadS3ConfigRaw(ctx context.Context) (*BackupS3Config, error) {
	raw, err := s.settingRepo.GetValue(ctx, settingKeyBackupS3Config)
	if err != nil {
		// setting 行不存在 = 尚未配置（合法状态）；其它错误（如 DB 故障）必须传播，
		// 否则 UpdateS3Config 留空 Secret 编辑时会静默覆盖既有凭证。
		if errors.Is(err, ErrSettingNotFound) {
			return nil, nil //nolint:nilnil // 尚未配置是合法状态
		}
		return nil, fmt.Errorf("read backup s3 config: %w", err)
	}
	if raw == "" {
		return nil, nil //nolint:nilnil // 尚未配置是合法状态
	}
	var cfg BackupS3Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, ErrBackupS3ConfigCorrupt
	}
	return &cfg, nil
}

// verifyBackupPrefixNotPublic 探测 PublicBaseURL 指向的域名是否允许匿名读取 backups/ 前缀。
//
// 这是 PublicBaseURL 场景下 backups/ 隔离的可验证安全边界：随机路径/日志/UI 警告都不能
// 阻止 R2 custom domain / public bucket 的匿名读取，只有 CDN/Worker 层的访问控制可以。
//
// SSRF 防护（复用项目已有逻辑）：
//   - 仅允许 HTTPS（urlvalidator.ValidateHTTPSURL）
//   - 拒绝 localhost / 私网 IP 字面量 / 云元数据 hostname（isBlockedHostname + urlvalidator）
//   - 传输层 safeDialContext 防止 DNS rebinding（dial 时再次校验解析 IP）
//   - 禁止重定向（CheckRedirect 返回 ErrUseLastResponse），防止重定向到内网
//   - 短超时（backupProbeTimeout），避免无限阻塞保存操作
//
// 探测方法：GET（带 Range: bytes=0-0）而非 HEAD。
// 某些 CDN/WAF 对 HEAD 返回 403 但允许 GET 回源，仅验证 HEAD 不能证明匿名 GET 被拒绝。
//
// 响应判定（fail-closed）：
//   - 403/401：访问被拒绝（Worker 已部署或 bucket 私有）→ 安全，返回 nil
//   - 404：访问被允许但 key 不存在 → DANGER：backups/ 公开可读
//   - 200/206：内容被返回（探测 key 不应命中真实对象）→ DANGER
//   - 3xx/5xx/其他：无法确认策略生效 → DANGER（fail-closed）
//   - 网络错误：CDN 不可达 → DANGER（fail-closed，不允许多 Saving 未验证的公开配置）
//
// 探测 key 使用 backups/.policy-probe-<uuid>，不会匹配任何真实备份
// （真实备份为 backups/{date}/{uuid}/{filename}）。
func (s *BackupService) verifyBackupPrefixNotPublic(ctx context.Context, publicBaseURL string) error {
	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	if base == "" {
		return nil // 未配置 PublicBaseURL，无需探测
	}

	// ── SSRF 防护：URL 格式 + scheme + host 校验 ──
	// ValidateHTTPSURL 强制 HTTPS、拒绝 localhost/私网 IP 字面量；
	// 额外用 isBlockedHostname 拒绝云元数据 hostname（metadata.google.internal 等）。
	validated, err := urlvalidator.ValidateHTTPSURL(base, urlvalidator.ValidationOptions{
		AllowPrivate: false,
	})
	if err != nil {
		return fmt.Errorf("%w: invalid public base url (must be HTTPS, public host, no private IP): %v",
			ErrBackupPrefixPubliclyReadable, err)
	}
	// 额外校验：提取 hostname 检查云元数据黑名单（urlvalidator 不覆盖）
	if u, perr := http.NewRequest(http.MethodGet, validated, nil); perr == nil {
		if isBlockedHostname(u.URL.Hostname()) {
			return fmt.Errorf("%w: hostname %q is blocked (cloud metadata / localhost)",
				ErrBackupPrefixPubliclyReadable, u.URL.Hostname())
		}
	}

	// ── 构造探测请求：GET + Range（而非 HEAD）──
	probeKey := "backups/.policy-probe-" + uuid.NewString()
	probeURL := validated + "/" + probeKey

	doer := s.probeDoer
	if doer == nil {
		doer = newBackupProbeHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return fmt.Errorf("%w: construct probe request: %v", ErrBackupPrefixPubliclyReadable, err)
	}
	// Range: bytes=0-0 限制响应体大小；某些 CDN 对 HEAD 返回 403 但允许 GET，
	// 必须用 GET 才能验证匿名读取是否真的被拒绝。
	req.Header.Set("Range", "bytes=0-0")

	resp, err := doer.Do(req)
	if err != nil {
		// 网络不可达（域名未解析、CDN 未配置、连接超时）→ fail-closed 拒绝保存。
		// 旧实现 warn-only 放行：若 CDN 暂不可达但稍后解析到公开 bucket，备份仍会公开。
		// 管理员须确保 CDN 域名可达且 Worker 已部署后再保存配置。
		return fmt.Errorf("%w: cannot reach PublicBaseURL to verify backups/ policy (%v) — "+
			"ensure the CDN domain is reachable and deploy/cloudflare-worker/r2-access-policy.js is deployed to deny backups/",
			ErrBackupPrefixPubliclyReadable, err)
	}
	defer resp.Body.Close()
	// 排空并限制 body 读取，防止恶意服务端通过 body 拖垮探测
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		// 访问被明确拒绝——策略生效（Worker 或私有 bucket）
		return nil
	case resp.StatusCode == http.StatusNotFound:
		// 404 表示访问被允许但 key 不存在：backups/ 公开可读
		return fmt.Errorf("%w: probe %s returned 404 (anonymous access allowed, key missing) — "+
			"deploy deploy/cloudflare-worker/r2-access-policy.js to deny backups/, or remove PublicBaseURL and use presigned URLs",
			ErrBackupPrefixPubliclyReadable, probeURL)
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent:
		// 200/206：内容被返回（探测 key 不应命中真实对象，命中说明 bucket 完全公开）
		return fmt.Errorf("%w: probe %s returned status %d (content served) — "+
			"backups/ is publicly readable; deploy CDN policy or remove PublicBaseURL",
			ErrBackupPrefixPubliclyReadable, probeURL, resp.StatusCode)
	default:
		// 3xx/5xx/其他：无法确认策略生效，视为危险（fail-closed）
		return fmt.Errorf("%w: probe %s returned unexpected status %d — "+
			"cannot verify backups/ is not publicly readable; deploy CDN policy or remove PublicBaseURL",
			ErrBackupPrefixPubliclyReadable, probeURL, resp.StatusCode)
	}
}

// newBackupProbeHTTPClient 构造 PublicBaseURL 探测专用的 SSRF 安全 HTTP 客户端。
//
// 安全特性：
//   - safeDialContext：dial 时校验解析 IP，防止 DNS rebinding 到私网（复用 channel_monitor_ssrf.go）
//   - CheckRedirect：禁止跟随重定向，防止重定向到内网地址
//   - Timeout：backupProbeTimeout（5s），避免无限阻塞保存操作
//
// 测试中通过注入 probeDoer mock 绕过此客户端，直接模拟响应。
func newBackupProbeHTTPClient() *http.Client {
	tr := &http.Transport{
		DialContext:       safeDialContext,
		ForceAttemptHTTP2: true,
		MaxIdleConns:      2,
	}
	return &http.Client{
		Timeout: backupProbeTimeout,
		Transport: tr,
		// 禁止重定向：探测不应跟随 3xx，防止重定向到内网或绕过校验。
		// 返回 ErrUseLastResponse 使客户端返回首条响应（含 3xx 状态码）供上层判定。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// normalizeBackupPrefix 强制规范化备份前缀为 "backups"。
//
// 这是前缀隔离的硬性边界保护：无论管理员如何配置，备份只使用 backups/ 前缀，
// 与 Agnes 图片（agnes-chat/）和 AI 生图（image-generation/）严格隔离。
//
// 早期实现仅拒绝与图片前缀完全相等的值，仍接受 agnes-chat/foo、image-generation/foo
// 或任意自定义值，导致备份可写入图片命名空间。现改为无条件返回 "backups"，
// Prefix 字段不再可配置（前端应展示为只读）。
func normalizeBackupPrefix(prefix string) string {
	_ = prefix
	return "backups"
}

func (s *BackupService) TestS3Connection(ctx context.Context, cfg BackupS3Config) error {
	// 强制规范化前缀，避免测试对象写入图片命名空间或根目录
	cfg.Prefix = normalizeBackupPrefix(cfg.Prefix)

	// 如果没提供 secret，用已保存的（传播读取错误，避免故障下使用空 Secret 测试）
	if cfg.SecretAccessKey == "" {
		old, err := s.loadS3Config(ctx)
		if err != nil {
			return fmt.Errorf("load saved s3 config: %w", err)
		}
		if old != nil {
			cfg.SecretAccessKey = old.SecretAccessKey
		}
	}

	if cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return fmt.Errorf("incomplete S3 config: bucket, access_key_id, secret_access_key are required")
	}

	store, err := s.storeFactory(ctx, &cfg)
	if err != nil {
		return err
	}

	// 1. 验证 bucket 可访问
	if err := store.HeadBucket(ctx); err != nil {
		return fmt.Errorf("bucket not accessible: %w", err)
	}

	// 2. 验证上传权限：写入一个临时测试对象（强制位于 backups/ 前缀下）
	testKey := fmt.Sprintf("%s/.s3-connection-test/%d-%d",
		cfg.Prefix, time.Now().UnixNano(), time.Now().UnixNano()%1000)
	testBody := []byte("s3 connection test")
	if _, err := store.Upload(ctx, testKey, bytes.NewReader(testBody), "text/plain"); err != nil {
		return fmt.Errorf("upload test object failed (permission denied?): %w", err)
	}

	// 3. 验证读取权限
	reader, err := store.Download(ctx, testKey)
	if err != nil {
		// 清理失败时记录日志但不掩盖原始错误
		if delErr := store.Delete(ctx, testKey); delErr != nil {
			logger.LegacyPrintf("service.backup", "[Backup] TestS3Connection: cleanup failed after download error: %v (test key: %s)", delErr, testKey)
		}
		return fmt.Errorf("download test object failed (read permission denied?): %w", err)
	}
	downloaded, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		if delErr := store.Delete(ctx, testKey); delErr != nil {
			logger.LegacyPrintf("service.backup", "[Backup] TestS3Connection: cleanup failed after read error: %v (test key: %s)", delErr, testKey)
		}
		return fmt.Errorf("read test object failed: %w", readErr)
	}
	if !bytes.Equal(downloaded, testBody) {
		if delErr := store.Delete(ctx, testKey); delErr != nil {
			logger.LegacyPrintf("service.backup", "[Backup] TestS3Connection: cleanup failed after content mismatch: %v (test key: %s)", delErr, testKey)
		}
		return fmt.Errorf("test object content mismatch: uploaded %d bytes, downloaded %d bytes", len(testBody), len(downloaded))
	}

	// 4. 验证删除权限
	if err := store.Delete(ctx, testKey); err != nil {
		return fmt.Errorf("delete test object failed (delete permission denied?): %w", err)
	}

	// 5. 验证所有公开入口的 backups/ 访问策略（CDN/Worker 边界）
	// S3 凭证与 bucket 权限正确，不代表公开域名安全。若任一公开入口允许匿名读取 backups/，
	// 数据库备份将公开暴露。必须探测 PublicBaseURL + AdditionalPublicBaseURLs 全部入口，
	// 避免 r2.dev / 未受 Worker 保护的 custom domain 绕过。
	//
	// ⚠️ 同 UpdateS3Config：声明式列表是 SECONDARY 防御，HARD 前提是 BucketPrivacyAttested。
	if cfg.PublicBaseURL != "" && !cfg.BucketPrivacyAttested {
		return ErrBucketPrivacyNotAttested
	}
	publicURLs := []string{cfg.PublicBaseURL}
	publicURLs = append(publicURLs, cfg.AdditionalPublicBaseURLs...)
	for _, u := range publicURLs {
		if u == "" {
			continue
		}
		if err := s.verifyBackupPrefixNotPublic(ctx, u); err != nil {
			return fmt.Errorf("verify public url %q: %w", u, err)
		}
	}

	return nil
}

// ─── 定时备份管理 ───

func (s *BackupService) GetSchedule(ctx context.Context) (*BackupScheduleConfig, error) {
	raw, err := s.settingRepo.GetValue(ctx, settingKeyBackupSchedule)
	if err != nil || raw == "" {
		return &BackupScheduleConfig{}, nil
	}
	var cfg BackupScheduleConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return &BackupScheduleConfig{}, nil
	}
	return &cfg, nil
}

func (s *BackupService) UpdateSchedule(ctx context.Context, cfg BackupScheduleConfig) (*BackupScheduleConfig, error) {
	if cfg.Enabled && cfg.CronExpr == "" {
		return nil, infraerrors.BadRequest("INVALID_CRON", "cron expression is required when schedule is enabled")
	}
	// 验证 cron 表达式
	if cfg.CronExpr != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(cfg.CronExpr); err != nil {
			return nil, infraerrors.BadRequest("INVALID_CRON", fmt.Sprintf("invalid cron expression: %v", err))
		}
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal schedule config: %w", err)
	}
	if err := s.settingRepo.Set(ctx, settingKeyBackupSchedule, string(data)); err != nil {
		return nil, fmt.Errorf("save schedule config: %w", err)
	}

	// 应用或停止定时任务
	if cfg.Enabled {
		if err := s.applyCronSchedule(&cfg); err != nil {
			return nil, err
		}
	} else {
		s.removeCronSchedule()
	}

	return &cfg, nil
}

func (s *BackupService) applyCronSchedule(cfg *BackupScheduleConfig) error {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()

	if s.cronSched == nil {
		return fmt.Errorf("cron scheduler not initialized")
	}

	// 移除旧任务
	if s.cronEntryID != 0 {
		s.cronSched.Remove(s.cronEntryID)
		s.cronEntryID = 0
	}

	entryID, err := s.cronSched.AddFunc(cfg.CronExpr, func() {
		s.runScheduledBackup()
	})
	if err != nil {
		return infraerrors.BadRequest("INVALID_CRON", fmt.Sprintf("failed to schedule: %v", err))
	}
	s.cronEntryID = entryID
	logger.LegacyPrintf("service.backup", "[Backup] 定时备份已启用: %s", cfg.CronExpr)
	return nil
}

func (s *BackupService) removeCronSchedule() {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	if s.cronSched != nil && s.cronEntryID != 0 {
		s.cronSched.Remove(s.cronEntryID)
		s.cronEntryID = 0
		logger.LegacyPrintf("service.backup", "[Backup] 定时备份已停用")
	}
}

func (s *BackupService) runScheduledBackup() {
	s.wg.Add(1)
	defer s.wg.Done()

	ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Minute)
	defer cancel()

	// 读取定时备份配置中的过期天数
	schedule, _ := s.GetSchedule(ctx)
	expireDays := 14 // 默认14天过期
	if schedule != nil && schedule.RetainDays > 0 {
		expireDays = schedule.RetainDays
	}

	logger.LegacyPrintf("service.backup", "[Backup] 开始执行定时备份, 过期天数: %d", expireDays)
	record, err := s.CreateBackup(ctx, "scheduled", expireDays)
	if err != nil {
		if errors.Is(err, ErrBackupInProgress) {
			logger.LegacyPrintf("service.backup", "[Backup] 定时备份跳过: 已有备份正在进行中")
		} else {
			logger.LegacyPrintf("service.backup", "[Backup] 定时备份失败: %v", err)
		}
		return
	}
	logger.LegacyPrintf("service.backup", "[Backup] 定时备份完成: id=%s size=%d", record.ID, record.SizeBytes)

	// 清理过期备份（复用已加载的 schedule）
	if schedule == nil {
		return
	}
	if err := s.cleanupOldBackups(ctx, schedule); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 清理过期备份失败: %v", err)
	}
}

// ─── 备份/恢复核心 ───

// CreateBackup 创建全量数据库备份并上传到 S3（流式处理）
// expireDays: 备份过期天数，0=永不过期，默认14天
func (s *BackupService) CreateBackup(ctx context.Context, triggeredBy string, expireDays int) (*BackupRecord, error) {
	if s.shuttingDown.Load() {
		return nil, infraerrors.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}

	s.opMu.Lock()
	if s.backingUp {
		s.opMu.Unlock()
		return nil, ErrBackupInProgress
	}
	s.backingUp = true
	s.opMu.Unlock()
	defer func() {
		s.opMu.Lock()
		s.backingUp = false
		s.opMu.Unlock()
	}()

	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	if s3Cfg == nil || !s3Cfg.IsConfigured() {
		return nil, ErrBackupS3NotConfigured
	}

	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return nil, fmt.Errorf("init object store: %w", err)
	}

	now := time.Now()
	backupID := uuid.New().String()[:8]
	fileName := fmt.Sprintf("%s_%s.sql.gz", s.dbCfg.DBName, now.Format("20060102_150405"))
	s3Key := s.buildS3Key(s3Cfg, fileName)

	var expiresAt string
	if expireDays > 0 {
		expiresAt = now.AddDate(0, 0, expireDays).Format(time.RFC3339)
	}

	record := &BackupRecord{
		ID:          backupID,
		Status:      "running",
		BackupType:  "postgres",
		FileName:    fileName,
		S3Key:       s3Key,
		TriggeredBy: triggeredBy,
		StartedAt:   now.Format(time.RFC3339),
		ExpiresAt:   expiresAt,
	}

	// 流式执行: pg_dump -> gzip -> S3 upload
	dumpReader, err := s.dumper.Dump(ctx)
	if err != nil {
		record.Status = "failed"
		record.ErrorMsg = fmt.Sprintf("pg_dump failed: %v", err)
		record.FinishedAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(ctx, record)
		return record, fmt.Errorf("pg_dump: %w", err)
	}

	// 使用 io.Pipe 将 gzip 压缩数据流式传递给 S3 上传
	pr, pw := io.Pipe()
	gzipDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				pw.CloseWithError(fmt.Errorf("gzip goroutine panic: %v", r)) //nolint:errcheck
				gzipDone <- fmt.Errorf("gzip goroutine panic: %v", r)
			}
		}()
		gzWriter := gzip.NewWriter(pw)
		var gzErr error
		_, gzErr = io.Copy(gzWriter, dumpReader)
		if closeErr := gzWriter.Close(); closeErr != nil && gzErr == nil {
			gzErr = closeErr
		}
		if closeErr := dumpReader.Close(); closeErr != nil && gzErr == nil {
			gzErr = closeErr
		}
		if gzErr != nil {
			_ = pw.CloseWithError(gzErr)
		} else {
			_ = pw.Close()
		}
		gzipDone <- gzErr
	}()

	contentType := "application/gzip"
	sizeBytes, err := objectStore.Upload(ctx, s3Key, pr, contentType)
	if err != nil {
		_ = pr.CloseWithError(err) // 确保 gzip goroutine 不会悬挂
		gzErr := <-gzipDone        // 安全等待 gzip goroutine 完成
		record.Status = "failed"
		errMsg := fmt.Sprintf("S3 upload failed: %v", err)
		if gzErr != nil {
			errMsg = fmt.Sprintf("gzip/dump failed: %v", gzErr)
		}
		record.ErrorMsg = errMsg
		record.FinishedAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(ctx, record)
		return record, fmt.Errorf("backup upload: %w", err)
	}
	<-gzipDone // 确保 gzip goroutine 已退出

	record.SizeBytes = sizeBytes
	record.Status = "completed"
	record.FinishedAt = time.Now().Format(time.RFC3339)
	if err := s.saveRecord(ctx, record); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 保存备份记录失败: %v", err)
	}

	return record, nil
}

// StartBackup 异步创建备份，立即返回 running 状态的记录
func (s *BackupService) StartBackup(ctx context.Context, triggeredBy string, expireDays int) (*BackupRecord, error) {
	if s.shuttingDown.Load() {
		return nil, infraerrors.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}

	s.opMu.Lock()
	if s.backingUp {
		s.opMu.Unlock()
		return nil, ErrBackupInProgress
	}
	s.backingUp = true
	s.opMu.Unlock()

	// 初始化阶段出错时自动重置标志
	launched := false
	defer func() {
		if !launched {
			s.opMu.Lock()
			s.backingUp = false
			s.opMu.Unlock()
		}
	}()

	// 在返回前加载 S3 配置和创建 store，避免 goroutine 中配置被修改
	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	if s3Cfg == nil || !s3Cfg.IsConfigured() {
		return nil, ErrBackupS3NotConfigured
	}

	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return nil, fmt.Errorf("init object store: %w", err)
	}

	now := time.Now()
	backupID := uuid.New().String()[:8]
	fileName := fmt.Sprintf("%s_%s.sql.gz", s.dbCfg.DBName, now.Format("20060102_150405"))
	s3Key := s.buildS3Key(s3Cfg, fileName)

	var expiresAt string
	if expireDays > 0 {
		expiresAt = now.AddDate(0, 0, expireDays).Format(time.RFC3339)
	}

	record := &BackupRecord{
		ID:          backupID,
		Status:      "running",
		BackupType:  "postgres",
		FileName:    fileName,
		S3Key:       s3Key,
		TriggeredBy: triggeredBy,
		StartedAt:   now.Format(time.RFC3339),
		ExpiresAt:   expiresAt,
		Progress:    "pending",
	}

	if err := s.saveRecord(ctx, record); err != nil {
		return nil, fmt.Errorf("save initial record: %w", err)
	}

	launched = true
	// 在启动 goroutine 前完成拷贝，避免数据竞争
	result := *record

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.opMu.Lock()
			s.backingUp = false
			s.opMu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				logger.LegacyPrintf("service.backup", "[Backup] panic recovered: %v", r)
				record.Status = "failed"
				record.ErrorMsg = fmt.Sprintf("internal panic: %v", r)
				record.Progress = ""
				record.FinishedAt = time.Now().Format(time.RFC3339)
				_ = s.saveRecord(context.Background(), record)
			}
		}()
		s.executeBackup(record, objectStore)
	}()

	return &result, nil
}

// executeBackup 后台执行备份（独立于 HTTP context）
func (s *BackupService) executeBackup(record *BackupRecord, objectStore BackupObjectStore) {
	ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Minute)
	defer cancel()

	// 阶段1: pg_dump
	record.Progress = "dumping"
	_ = s.saveRecord(ctx, record)

	dumpReader, err := s.dumper.Dump(ctx)
	if err != nil {
		record.Status = "failed"
		record.ErrorMsg = fmt.Sprintf("pg_dump failed: %v", err)
		record.Progress = ""
		record.FinishedAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(context.Background(), record)
		return
	}

	// 阶段2: gzip + upload
	record.Progress = "uploading"
	_ = s.saveRecord(ctx, record)

	pr, pw := io.Pipe()
	gzipDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				pw.CloseWithError(fmt.Errorf("gzip goroutine panic: %v", r)) //nolint:errcheck
				gzipDone <- fmt.Errorf("gzip goroutine panic: %v", r)
			}
		}()
		gzWriter := gzip.NewWriter(pw)
		var gzErr error
		_, gzErr = io.Copy(gzWriter, dumpReader)
		if closeErr := gzWriter.Close(); closeErr != nil && gzErr == nil {
			gzErr = closeErr
		}
		if closeErr := dumpReader.Close(); closeErr != nil && gzErr == nil {
			gzErr = closeErr
		}
		if gzErr != nil {
			_ = pw.CloseWithError(gzErr)
		} else {
			_ = pw.Close()
		}
		gzipDone <- gzErr
	}()

	contentType := "application/gzip"
	sizeBytes, err := objectStore.Upload(ctx, record.S3Key, pr, contentType)
	if err != nil {
		_ = pr.CloseWithError(err) // 确保 gzip goroutine 不会悬挂
		gzErr := <-gzipDone        // 安全等待 gzip goroutine 完成
		record.Status = "failed"
		errMsg := fmt.Sprintf("S3 upload failed: %v", err)
		if gzErr != nil {
			errMsg = fmt.Sprintf("gzip/dump failed: %v", gzErr)
		}
		record.ErrorMsg = errMsg
		record.Progress = ""
		record.FinishedAt = time.Now().Format(time.RFC3339)
		_ = s.saveRecord(context.Background(), record)
		return
	}
	<-gzipDone // 确保 gzip goroutine 已退出

	record.SizeBytes = sizeBytes
	record.Status = "completed"
	record.Progress = ""
	record.FinishedAt = time.Now().Format(time.RFC3339)
	if err := s.saveRecord(context.Background(), record); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 保存备份记录失败: %v", err)
	}
}

// RestoreBackup 从 S3 下载备份并流式恢复到数据库
func (s *BackupService) RestoreBackup(ctx context.Context, backupID string) error {
	s.opMu.Lock()
	if s.restoring {
		s.opMu.Unlock()
		return ErrRestoreInProgress
	}
	s.restoring = true
	s.opMu.Unlock()
	defer func() {
		s.opMu.Lock()
		s.restoring = false
		s.opMu.Unlock()
	}()

	record, err := s.GetBackupRecord(ctx, backupID)
	if err != nil {
		return err
	}
	if record.Status != "completed" {
		return infraerrors.BadRequest("BACKUP_NOT_COMPLETED", "can only restore from a completed backup")
	}

	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return err
	}
	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return fmt.Errorf("init object store: %w", err)
	}

	// 从 S3 流式下载
	body, err := objectStore.Download(ctx, record.S3Key)
	if err != nil {
		return fmt.Errorf("S3 download failed: %w", err)
	}
	defer func() { _ = body.Close() }()

	// 流式解压 gzip -> psql（不将全部数据加载到内存）
	gzReader, err := gzip.NewReader(body)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gzReader.Close() }()

	// 流式恢复
	if err := s.dumper.Restore(ctx, gzReader); err != nil {
		return fmt.Errorf("pg restore: %w", err)
	}

	return nil
}

// StartRestore 异步恢复备份，立即返回
func (s *BackupService) StartRestore(ctx context.Context, backupID string) (*BackupRecord, error) {
	if s.shuttingDown.Load() {
		return nil, infraerrors.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}

	s.opMu.Lock()
	if s.restoring {
		s.opMu.Unlock()
		return nil, ErrRestoreInProgress
	}
	s.restoring = true
	s.opMu.Unlock()

	// 初始化阶段出错时自动重置标志
	launched := false
	defer func() {
		if !launched {
			s.opMu.Lock()
			s.restoring = false
			s.opMu.Unlock()
		}
	}()

	record, err := s.GetBackupRecord(ctx, backupID)
	if err != nil {
		return nil, err
	}
	if record.Status != "completed" {
		return nil, infraerrors.BadRequest("BACKUP_NOT_COMPLETED", "can only restore from a completed backup")
	}

	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return nil, err
	}
	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return nil, fmt.Errorf("init object store: %w", err)
	}

	record.RestoreStatus = "running"
	_ = s.saveRecord(ctx, record)

	launched = true
	result := *record

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.opMu.Lock()
			s.restoring = false
			s.opMu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				logger.LegacyPrintf("service.backup", "[Backup] restore panic recovered: %v", r)
				record.RestoreStatus = "failed"
				record.RestoreError = fmt.Sprintf("internal panic: %v", r)
				_ = s.saveRecord(context.Background(), record)
			}
		}()
		s.executeRestore(record, objectStore)
	}()

	return &result, nil
}

// executeRestore 后台执行恢复
func (s *BackupService) executeRestore(record *BackupRecord, objectStore BackupObjectStore) {
	ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Minute)
	defer cancel()

	body, err := objectStore.Download(ctx, record.S3Key)
	if err != nil {
		record.RestoreStatus = "failed"
		record.RestoreError = fmt.Sprintf("S3 download failed: %v", err)
		_ = s.saveRecord(context.Background(), record)
		return
	}
	defer func() { _ = body.Close() }()

	gzReader, err := gzip.NewReader(body)
	if err != nil {
		record.RestoreStatus = "failed"
		record.RestoreError = fmt.Sprintf("gzip reader: %v", err)
		_ = s.saveRecord(context.Background(), record)
		return
	}
	defer func() { _ = gzReader.Close() }()

	if err := s.dumper.Restore(ctx, gzReader); err != nil {
		record.RestoreStatus = "failed"
		record.RestoreError = fmt.Sprintf("pg restore: %v", err)
		_ = s.saveRecord(context.Background(), record)
		return
	}

	record.RestoreStatus = "completed"
	record.RestoredAt = time.Now().Format(time.RFC3339)
	if err := s.saveRecord(context.Background(), record); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] 保存恢复记录失败: %v", err)
	}
}

// ─── 备份记录管理 ───

func (s *BackupService) ListBackups(ctx context.Context) ([]BackupRecord, error) {
	records, err := s.loadRecords(ctx)
	if err != nil {
		return nil, err
	}
	// 倒序返回（最新在前）
	sort.Slice(records, func(i, j int) bool {
		return records[i].StartedAt > records[j].StartedAt
	})
	return records, nil
}

func (s *BackupService) GetBackupRecord(ctx context.Context, backupID string) (*BackupRecord, error) {
	records, err := s.loadRecords(ctx)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].ID == backupID {
			return &records[i], nil
		}
	}
	return nil, ErrBackupNotFound
}

func (s *BackupService) DeleteBackup(ctx context.Context, backupID string) error {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()

	records, err := s.loadRecordsLocked(ctx)
	if err != nil {
		return err
	}

	var found *BackupRecord
	var remaining []BackupRecord
	for i := range records {
		if records[i].ID == backupID {
			found = &records[i]
		} else {
			remaining = append(remaining, records[i])
		}
	}
	if found == nil {
		return ErrBackupNotFound
	}

	// 从 S3 删除
	if found.S3Key != "" && found.Status == "completed" {
		// 安全检查：验证 key 位于 backups/ 前缀下，防止跨前缀误删
		if err := assertBackupKeyPrefix(found.S3Key); err != nil {
			logger.LegacyPrintf("service.backup", "[Backup] refused to delete key outside backup prefix: %s (backup: %s)", found.S3Key, backupID)
		} else {
			s3Cfg, err := s.loadS3Config(ctx)
			if err == nil && s3Cfg != nil && s3Cfg.IsConfigured() {
				objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
				if err == nil {
					if delErr := objectStore.Delete(ctx, found.S3Key); delErr != nil {
						logger.LegacyPrintf("service.backup", "[Backup] failed to delete S3 object %s: %v", found.S3Key, delErr)
					}
				}
			}
		}
	}

	return s.saveRecordsLocked(ctx, remaining)
}

// GetBackupDownloadURL 获取备份文件预签名下载 URL
func (s *BackupService) GetBackupDownloadURL(ctx context.Context, backupID string) (string, error) {
	record, err := s.GetBackupRecord(ctx, backupID)
	if err != nil {
		return "", err
	}
	if record.Status != "completed" {
		return "", infraerrors.BadRequest("BACKUP_NOT_COMPLETED", "backup is not completed")
	}

	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil {
		return "", err
	}
	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return "", err
	}

	url, err := objectStore.PresignURL(ctx, record.S3Key, 1*time.Hour)
	if err != nil {
		return "", fmt.Errorf("presign url: %w", err)
	}
	return url, nil
}

// ─── 内部方法 ───

func (s *BackupService) loadS3Config(ctx context.Context) (*BackupS3Config, error) {
	raw, err := s.settingRepo.GetValue(ctx, settingKeyBackupS3Config)
	if err != nil {
		// setting 行不存在 = 尚未配置（合法状态）；其它错误（如 DB 故障）必须传播。
		if errors.Is(err, ErrSettingNotFound) {
			return nil, nil //nolint:nilnil // no config is a valid state
		}
		return nil, fmt.Errorf("read backup s3 config: %w", err)
	}
	if raw == "" {
		return nil, nil //nolint:nilnil // no config is a valid state
	}
	var cfg BackupS3Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, ErrBackupS3ConfigCorrupt
	}
	// 解密 SecretAccessKey
	if cfg.SecretAccessKey != "" {
		decrypted, err := s.encryptor.Decrypt(cfg.SecretAccessKey)
		if err != nil {
			// 兼容未加密的旧数据：如果解密失败，保持原值
			logger.LegacyPrintf("service.backup", "[Backup] S3 SecretAccessKey 解密失败（可能是旧的未加密数据）: %v", err)
		} else {
			cfg.SecretAccessKey = decrypted
		}
	}
	return &cfg, nil
}

func (s *BackupService) getOrCreateStore(ctx context.Context, cfg *BackupS3Config) (BackupObjectStore, error) {
	if cfg == nil {
		return nil, ErrBackupS3NotConfigured
	}

	sig := backupConfigSignature(cfg)

	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	// 配置指纹比对：本实例若已缓存客户端且配置未变，则复用；否则重建。
	// 注意：多实例下其他实例不会收到本实例的缓存失效信号，因此指纹比对是必要的——
	// 每次调用都会从数据库重新加载配置（通过 loadS3Config），签名变化时自动重建。
	if s.store != nil && s.s3Cfg != nil && s.storeSig == sig {
		return s.store, nil
	}

	store, err := s.storeFactory(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.store = store
	s.s3Cfg = cfg
	s.storeSig = sig
	return store, nil
}

// backupConfigSignature 计算备份 S3 配置指纹，用于检测配置变更。
// 包含所有影响 S3 客户端构造的字段（含 SecretAccessKey）。
func backupConfigSignature(cfg *BackupS3Config) string {
	if cfg == nil {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s|%v",
		cfg.Endpoint, cfg.Region, cfg.Bucket,
		cfg.AccessKeyID, cfg.SecretAccessKey,
		cfg.ForcePathStyle,
	)
}

func (s *BackupService) buildS3Key(cfg *BackupS3Config, fileName string) string {
	// 强制规范化：无论 cfg.Prefix 是什么，备份 key 必定以 backups/ 开头
	prefix := normalizeBackupPrefix(cfg.Prefix)
	// 插入随机目录段使备份路径不可预测，作为 PublicBaseURL 公开桶场景下的纵深防御：
	// 即使 bucket 通过 R2 custom domain 公开，攻击者也无法枚举/猜测 backups/ 下的对象路径
	// （文件名本身含可预测的 {dbname}_{timestamp}，单纯依赖路径保密并不可靠，
	// 但随机段使批量扫描不可行）。真正的安全边界仍需在 CDN/Worker 层显式拒绝 backups/。
	return fmt.Sprintf("%s/%s/%s/%s", prefix, time.Now().Format("2006/01/02"), uuid.NewString(), fileName)
}

// assertBackupKeyPrefix 验证 S3 key 位于 backups/ 前缀下，防止跨前缀误删。
// 用于 DeleteBackup 和 cleanupOldBackups 的安全检查。
func assertBackupKeyPrefix(key string) error {
	normalized := normalizeBackupPrefix("") // "backups"
	expected := normalized + "/"
	if !strings.HasPrefix(key, expected) {
		return fmt.Errorf("refused to delete key outside backup prefix: %s (expected prefix: %s)", key, expected)
	}
	return nil
}

// loadRecords 加载备份记录，区分"无数据"和"数据损坏"
func (s *BackupService) loadRecords(ctx context.Context) ([]BackupRecord, error) {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	return s.loadRecordsLocked(ctx)
}

// loadRecordsLocked 在已持有 recordsMu 锁的情况下加载记录
func (s *BackupService) loadRecordsLocked(ctx context.Context) ([]BackupRecord, error) {
	raw, err := s.settingRepo.GetValue(ctx, settingKeyBackupRecords)
	if err != nil || raw == "" {
		return nil, nil //nolint:nilnil // no records is a valid state
	}
	var records []BackupRecord
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return nil, ErrBackupRecordsCorrupt
	}
	return records, nil
}

// saveRecordsLocked 在已持有 recordsMu 锁的情况下保存记录
func (s *BackupService) saveRecordsLocked(ctx context.Context, records []BackupRecord) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, settingKeyBackupRecords, string(data))
}

// saveRecord 保存单条记录（带互斥锁保护）
func (s *BackupService) saveRecord(ctx context.Context, record *BackupRecord) error {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()

	records, _ := s.loadRecordsLocked(ctx)

	// 更新已有记录或追加
	found := false
	for i := range records {
		if records[i].ID == record.ID {
			records[i] = *record
			found = true
			break
		}
	}
	if !found {
		records = append(records, *record)
	}

	// 限制记录数量
	if len(records) > maxBackupRecords {
		records = records[len(records)-maxBackupRecords:]
	}

	return s.saveRecordsLocked(ctx, records)
}

func (s *BackupService) cleanupOldBackups(ctx context.Context, schedule *BackupScheduleConfig) error {
	if schedule == nil {
		return nil
	}

	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()

	records, err := s.loadRecordsLocked(ctx)
	if err != nil {
		return err
	}

	// 按时间倒序
	sort.Slice(records, func(i, j int) bool {
		return records[i].StartedAt > records[j].StartedAt
	})

	var toDelete []BackupRecord
	var toKeep []BackupRecord

	for i, r := range records {
		shouldDelete := false

		// 按保留份数清理
		if schedule.RetainCount > 0 && i >= schedule.RetainCount {
			shouldDelete = true
		}

		// 按保留天数清理
		if schedule.RetainDays > 0 && r.StartedAt != "" {
			startedAt, err := time.Parse(time.RFC3339, r.StartedAt)
			if err == nil && time.Since(startedAt) > time.Duration(schedule.RetainDays)*24*time.Hour {
				shouldDelete = true
			}
		}

		if shouldDelete && r.Status == "completed" {
			toDelete = append(toDelete, r)
		} else {
			toKeep = append(toKeep, r)
		}
	}

	// 删除 S3 上的文件
	for _, r := range toDelete {
		if r.S3Key != "" {
			_ = s.deleteS3Object(ctx, r.S3Key)
		}
	}

	if len(toDelete) > 0 {
		logger.LegacyPrintf("service.backup", "[Backup] 自动清理了 %d 个过期备份", len(toDelete))
		return s.saveRecordsLocked(ctx, toKeep)
	}
	return nil
}

func (s *BackupService) deleteS3Object(ctx context.Context, key string) error {
	// 安全检查：验证 key 位于 backups/ 前缀下，防止跨前缀误删
	if err := assertBackupKeyPrefix(key); err != nil {
		logger.LegacyPrintf("service.backup", "[Backup] refused to delete key outside backup prefix: %s", key)
		return err
	}
	s3Cfg, err := s.loadS3Config(ctx)
	if err != nil || s3Cfg == nil {
		return nil
	}
	objectStore, err := s.getOrCreateStore(ctx, s3Cfg)
	if err != nil {
		return err
	}
	return objectStore.Delete(ctx, key)
}
