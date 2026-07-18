//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ─── Mocks ───

type mockSettingRepo struct {
	mu          sync.Mutex
	data        map[string]string
	getValueErr error // 非空时 GetValue 返回该错误，模拟 settings repository 读取失败
}

func newMockSettingRepo() *mockSettingRepo {
	return &mockSettingRepo{data: make(map[string]string)}
}

func (m *mockSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: v}, nil
}

func (m *mockSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getValueErr != nil {
		return "", m.getValueErr
	}
	v, ok := m.data[key]
	if !ok {
		// 与真实 settingRepository.GetValue 对齐：行不存在时返回 ErrSettingNotFound，
		// 而非 ("", nil)，避免掩盖 loadS3Config 的 "尚未配置" 分支回归。
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (m *mockSettingRepo) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *mockSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string)
	for _, k := range keys {
		if v, ok := m.data[k]; ok {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range settings {
		m.data[k] = v
	}
	return nil
}

func (m *mockSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string, len(m.data))
	for k, v := range m.data {
		result[k] = v
	}
	return result, nil
}

func (m *mockSettingRepo) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// plainEncryptor 仅做 base64-like 包装，用于测试
type plainEncryptor struct{}

func (e *plainEncryptor) Encrypt(plaintext string) (string, error) {
	return "ENC:" + plaintext, nil
}

func (e *plainEncryptor) Decrypt(ciphertext string) (string, error) {
	if strings.HasPrefix(ciphertext, "ENC:") {
		return strings.TrimPrefix(ciphertext, "ENC:"), nil
	}
	return ciphertext, fmt.Errorf("not encrypted")
}

// mockProbeDoer 模拟 verifyBackupPrefixNotPublic 的 HTTP 客户端，避免真实网络调用。
// status 为返回的状态码；err 非 nil 时直接返回该错误（模拟网络不可达）。
type mockProbeDoer struct {
	status     int    // 返回的 HTTP 状态码（err 为空时生效）
	err        error  // 非 nil 时 Do 返回此错误
	lastURL    string // 记录最后一次请求 URL，供测试断言探测路径
	lastMethod string // 记录最后一次请求方法
	lastRange  string // 记录最后一次请求的 Range 头
}

func (m *mockProbeDoer) Do(req *http.Request) (*http.Response, error) {
	m.lastURL = req.URL.String()
	m.lastMethod = req.Method
	m.lastRange = req.Header.Get("Range")
	if m.err != nil {
		return nil, m.err
	}
	return &http.Response{StatusCode: m.status, Body: http.NoBody, Header: make(http.Header)}, nil
}

// perHostMockProbeDoer 按请求 host 返回不同响应，用于多域名探测测试：
// 例如 cdn.example.com（受 Worker 保护）返回 403，pub-abc.r2.dev（未保护）返回 404。
type perHostMockProbeDoer struct {
	byHost map[string]int   // host → 状态码
	errs   map[string]error // host → 网络错误（优先于 byHost）
}

func (m *perHostMockProbeDoer) Do(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if err, ok := m.errs[host]; ok {
		return nil, err
	}
	if status, ok := m.byHost[host]; ok {
		return &http.Response{StatusCode: status, Body: http.NoBody, Header: make(http.Header)}, nil
	}
	// 默认 403
	return &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody, Header: make(http.Header)}, nil
}

type mockDumper struct {
	dumpData []byte
	dumpErr  error
	restored []byte
	restErr  error
}

func (m *mockDumper) Dump(_ context.Context) (io.ReadCloser, error) {
	if m.dumpErr != nil {
		return nil, m.dumpErr
	}
	return io.NopCloser(bytes.NewReader(m.dumpData)), nil
}

func (m *mockDumper) Restore(_ context.Context, data io.Reader) error {
	if m.restErr != nil {
		return m.restErr
	}
	d, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	m.restored = d
	return nil
}

// blockingDumper 可控延迟的 dumper，用于测试异步行为
type blockingDumper struct {
	blockCh chan struct{}
	data    []byte
	restErr error
}

func (d *blockingDumper) Dump(ctx context.Context) (io.ReadCloser, error) {
	select {
	case <-d.blockCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return io.NopCloser(bytes.NewReader(d.data)), nil
}

func (d *blockingDumper) Restore(_ context.Context, data io.Reader) error {
	if d.restErr != nil {
		return d.restErr
	}
	_, _ = io.ReadAll(data)
	return nil
}

type mockObjectStore struct {
	objects map[string][]byte
	mu      sync.Mutex
}

func newMockObjectStore() *mockObjectStore {
	return &mockObjectStore{objects: make(map[string][]byte)}
}

func (m *mockObjectStore) Upload(_ context.Context, key string, body io.Reader, _ string) (int64, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.objects[key] = data
	m.mu.Unlock()
	return int64(len(data)), nil
}

func (m *mockObjectStore) Download(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.objects, key)
	m.mu.Unlock()
	return nil
}

func (m *mockObjectStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://presigned.example.com/" + key, nil
}

func (m *mockObjectStore) HeadBucket(_ context.Context) error {
	return nil
}

func newTestBackupService(repo *mockSettingRepo, dumper DBDumper, store *mockObjectStore) *BackupService {
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Host:   "localhost",
			Port:   5432,
			User:   "test",
			DBName: "testdb",
		},
	}
	factory := func(_ context.Context, _ *BackupS3Config) (BackupObjectStore, error) {
		return store, nil
	}
	return NewBackupService(repo, cfg, &plainEncryptor{}, factory, dumper)
}

func seedS3Config(t *testing.T, repo *mockSettingRepo) {
	t.Helper()
	cfg := BackupS3Config{
		Bucket:          "test-bucket",
		AccessKeyID:     "AKID",
		SecretAccessKey: "ENC:secret123",
		Prefix:          "backups",
		PublicBaseURL:   "https://cdn.example.com",
	}
	data, _ := json.Marshal(cfg)
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, string(data)))
}

// ─── Tests ───

func TestBackupService_S3ConfigEncryption(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 保存配置 -> SecretAccessKey 应被加密
	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:          "my-bucket",
		AccessKeyID:     "AKID",
		SecretAccessKey: "my-secret",
		Prefix:          "backups",
	})
	require.NoError(t, err)

	// 直接读取数据库中存储的值，应该是加密后的
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	var stored BackupS3Config
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, "ENC:my-secret", stored.SecretAccessKey)

	// 通过 GetS3Config 获取应该脱敏
	cfg, err := svc.GetS3Config(context.Background())
	require.NoError(t, err)
	require.Empty(t, cfg.SecretAccessKey)
	require.Equal(t, "my-bucket", cfg.Bucket)

	// loadS3Config 内部应解密
	internal, err := svc.loadS3Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "my-secret", internal.SecretAccessKey)
}

func TestBackupService_S3ConfigKeepExistingSecret(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 先保存一个有 secret 的配置
	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:          "my-bucket",
		AccessKeyID:     "AKID",
		SecretAccessKey: "original-secret",
	})
	require.NoError(t, err)

	// 再更新时不提供 secret，应保留原值
	_, err = svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:      "my-bucket",
		AccessKeyID: "AKID-NEW",
	})
	require.NoError(t, err)

	internal, err := svc.loadS3Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "original-secret", internal.SecretAccessKey)
	require.Equal(t, "AKID-NEW", internal.AccessKeyID)
}

func TestBackupService_SaveRecordConcurrency(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	var wg sync.WaitGroup
	n := 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			record := &BackupRecord{
				ID:        fmt.Sprintf("rec-%d", idx),
				Status:    "completed",
				StartedAt: time.Now().Format(time.RFC3339),
			}
			_ = svc.saveRecord(context.Background(), record)
		}(i)
	}
	wg.Wait()

	records, err := svc.loadRecords(context.Background())
	require.NoError(t, err)
	require.Len(t, records, n)
}

func TestBackupService_LoadRecords_Empty(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	records, err := svc.loadRecords(context.Background())
	require.NoError(t, err)
	require.Nil(t, records) // 无数据时返回 nil
}

func TestBackupService_LoadRecords_Corrupted(t *testing.T) {
	repo := newMockSettingRepo()
	_ = repo.Set(context.Background(), settingKeyBackupRecords, "not valid json{{{")
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	records, err := svc.loadRecords(context.Background())
	require.Error(t, err) // 损坏数据应返回错误
	require.Nil(t, records)
}

func TestBackupService_CreateBackup_Streaming(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumpContent := "-- PostgreSQL dump\nCREATE TABLE test (id int);\n"
	dumper := &mockDumper{dumpData: []byte(dumpContent)}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	require.Equal(t, "completed", record.Status)
	require.Greater(t, record.SizeBytes, int64(0))
	require.NotEmpty(t, record.S3Key)

	// 验证 S3 上确实有文件
	store.mu.Lock()
	require.Len(t, store.objects, 1)
	store.mu.Unlock()
}

func TestBackupService_CreateBackup_DumpFailure(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &mockDumper{dumpErr: fmt.Errorf("pg_dump failed")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.Error(t, err)
	require.Equal(t, "failed", record.Status)
	require.Contains(t, record.ErrorMsg, "pg_dump")
}

func TestBackupService_CreateBackup_NoS3Config(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	_, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.ErrorIs(t, err, ErrBackupS3NotConfigured)
}

func TestBackupService_CreateBackup_ConcurrentBlocked(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	// 使用一个慢速 dumper 来模拟正在进行的备份
	dumper := &mockDumper{dumpData: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	// 手动设置 backingUp 标志
	svc.opMu.Lock()
	svc.backingUp = true
	svc.opMu.Unlock()

	_, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.ErrorIs(t, err, ErrBackupInProgress)
}

func TestBackupService_RestoreBackup_Streaming(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumpContent := "-- PostgreSQL dump\nCREATE TABLE test (id int);\n"
	dumper := &mockDumper{dumpData: []byte(dumpContent)}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	// 先创建一个备份
	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// 恢复
	err = svc.RestoreBackup(context.Background(), record.ID)
	require.NoError(t, err)

	// 验证 psql 收到的数据是否与原始 dump 内容一致
	require.Equal(t, dumpContent, string(dumper.restored))
}

func TestBackupService_RestoreBackup_NotCompleted(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 手动插入一条 failed 记录
	_ = svc.saveRecord(context.Background(), &BackupRecord{
		ID:     "fail-1",
		Status: "failed",
	})

	err := svc.RestoreBackup(context.Background(), "fail-1")
	require.Error(t, err)
}

func TestBackupService_DeleteBackup(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumpContent := "data"
	dumper := &mockDumper{dumpData: []byte(dumpContent)}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// S3 中应有文件
	store.mu.Lock()
	require.Len(t, store.objects, 1)
	store.mu.Unlock()

	// 删除
	err = svc.DeleteBackup(context.Background(), record.ID)
	require.NoError(t, err)

	// S3 中文件应被删除
	store.mu.Lock()
	require.Len(t, store.objects, 0)
	store.mu.Unlock()

	// 记录应不存在
	_, err = svc.GetBackupRecord(context.Background(), record.ID)
	require.ErrorIs(t, err, ErrBackupNotFound)
}

func TestBackupService_GetDownloadURL(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &mockDumper{dumpData: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	url, err := svc.GetBackupDownloadURL(context.Background(), record.ID)
	require.NoError(t, err)
	require.Contains(t, url, "https://presigned.example.com/")
}

func TestBackupService_ListBackups_Sorted(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	now := time.Now()
	for i := 0; i < 3; i++ {
		_ = svc.saveRecord(context.Background(), &BackupRecord{
			ID:        fmt.Sprintf("rec-%d", i),
			Status:    "completed",
			StartedAt: now.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
		})
	}

	records, err := svc.ListBackups(context.Background())
	require.NoError(t, err)
	require.Len(t, records, 3)
	// 最新在前
	require.Equal(t, "rec-2", records[0].ID)
	require.Equal(t, "rec-0", records[2].ID)
}

func TestBackupService_TestS3Connection(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	// 注入 mock 探测器返回 403（CDN 策略生效），避免 PublicBaseURL 触发真实 HTTP 调用
	svc.probeDoer = &mockProbeDoer{status: http.StatusForbidden}

	// 完整测试：HeadBucket + Upload + Download（内容校验）+ Delete
	err := svc.TestS3Connection(context.Background(), BackupS3Config{
		Bucket:                "test",
		AccessKeyID:           "ak",
		SecretAccessKey:       "sk",
		Prefix:                "backups",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: true,
	})
	require.NoError(t, err)

	// 测试后端临时对象应已被清理
	store.mu.Lock()
	objCount := len(store.objects)
	store.mu.Unlock()
	require.Zero(t, objCount, "test objects should be deleted after connection test")
}

func TestBackupService_TestS3Connection_Incomplete(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	err := svc.TestS3Connection(context.Background(), BackupS3Config{
		Bucket: "test",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "incomplete")
}

// TestBackupService_TestS3Connection_KeepsExistingSecret 验证编辑时留空代表保持原值。
func TestBackupService_TestS3Connection_KeepsExistingSecret(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 不传 SecretAccessKey：应使用已保存的配置中的密钥（"ENC:secret123" 被解密为 "secret123"）
	err := svc.TestS3Connection(context.Background(), BackupS3Config{
		Bucket:      "test-bucket",
		AccessKeyID: "AKID",
		// SecretAccessKey 留空
	})
	require.NoError(t, err)
}

// TestBackupService_GetSharedObjectStorageConfig 验证 Agnes 与备份读取同一配置。
func TestBackupService_GetSharedObjectStorageConfig(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	cfg, err := svc.GetSharedObjectStorageConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, "test-bucket", cfg.Bucket)
	require.Equal(t, "AKID", cfg.AccessKeyID)
	require.Equal(t, "https://cdn.example.com", cfg.PublicBaseURL)
	// 返回的配置应包含解密后的 SecretAccessKey（供 Agnes 构造 S3 客户端）
	require.Equal(t, "secret123", cfg.SecretAccessKey)
	// Prefix 字段是备份用的前缀，Agnes 不读取此字段（使用固定的 AgnesChatPrefix）
	require.Equal(t, "backups", cfg.Prefix)
}

// TestBackupService_GetSharedObjectStorageConfig_NotConfigured 验证未配置时返回 nil（不报错）。
func TestBackupService_GetSharedObjectStorageConfig_NotConfigured(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	cfg, err := svc.GetSharedObjectStorageConfig(context.Background())
	require.NoError(t, err)
	require.Nil(t, cfg, "未配置时应返回 nil 而非报错")
}

// TestBackupService_GetSharedObjectStorageConfig_Incomplete 验证字段不完整时返回 nil。
func TestBackupService_GetSharedObjectStorageConfig_Incomplete(t *testing.T) {
	repo := newMockSettingRepo()
	// 只配置 Bucket，缺少 AccessKeyID/SecretAccessKey
	cfg := BackupS3Config{Bucket: "test-bucket"}
	data, _ := json.Marshal(cfg)
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, string(data)))

	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	got, err := svc.GetSharedObjectStorageConfig(context.Background())
	require.NoError(t, err)
	require.Nil(t, got, "字段不完整时 IsConfigured()=false，应返回 nil")
}

// TestBackupService_GetS3Config_RedactsSecret 验证查询接口脱敏 SecretAccessKey。
func TestBackupService_GetS3Config_RedactsSecret(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	cfg, err := svc.GetS3Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "", cfg.SecretAccessKey, "GetS3Config 必须脱敏 SecretAccessKey")
	require.Equal(t, "https://cdn.example.com", cfg.PublicBaseURL)
}

// TestBackupService_GetS3Config_NotConfigured 验证 settings 表无 backup_s3_config 行时
// （即 settingRepo.GetValue 返回 ErrSettingNotFound），GetS3Config 返回空对象而非 404。
// 这是生产环境 "SETTING_NOT_FOUND" 报错的回归测试。
func TestBackupService_GetS3Config_NotConfigured(t *testing.T) {
	repo := newMockSettingRepo() // 空 repo，无任何配置
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	cfg, err := svc.GetS3Config(context.Background())
	require.NoError(t, err, "尚未配置是合法状态，不得返回 ErrSettingNotFound")
	require.NotNil(t, cfg, "未配置时应返回空对象而非 nil")
	require.Equal(t, "", cfg.Bucket)
	require.Equal(t, "", cfg.SecretAccessKey)
}

func TestBackupService_Schedule_CronValidation(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	svc.cronSched = nil // 未初始化 cron

	// 启用但 cron 为空
	_, err := svc.UpdateSchedule(context.Background(), BackupScheduleConfig{
		Enabled:  true,
		CronExpr: "",
	})
	require.Error(t, err)

	// 无效的 cron 表达式
	_, err = svc.UpdateSchedule(context.Background(), BackupScheduleConfig{
		Enabled:  true,
		CronExpr: "invalid",
	})
	require.Error(t, err)
}

func TestBackupService_LoadS3Config_Corrupted(t *testing.T) {
	repo := newMockSettingRepo()
	_ = repo.Set(context.Background(), settingKeyBackupS3Config, "not json!!!!")
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	cfg, err := svc.loadS3Config(context.Background())
	require.Error(t, err)
	require.Nil(t, cfg)
}

// ─── Async Backup Tests ───

func TestStartBackup_ReturnsImmediately(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &blockingDumper{blockCh: make(chan struct{}), data: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	require.Equal(t, "running", record.Status)
	require.NotEmpty(t, record.ID)

	// 释放 dumper 让后台完成
	close(dumper.blockCh)
	svc.wg.Wait()

	// 验证最终状态
	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", final.Status)
	require.Greater(t, final.SizeBytes, int64(0))
}

func TestStartBackup_ConcurrentBlocked(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &blockingDumper{blockCh: make(chan struct{}), data: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	// 第一次启动
	_, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// 第二次应被阻塞
	_, err = svc.StartBackup(context.Background(), "manual", 14)
	require.ErrorIs(t, err, ErrBackupInProgress)

	close(dumper.blockCh)
	svc.wg.Wait()
}

func TestStartBackup_ShuttingDown(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{dumpData: []byte("data")}, newMockObjectStore())

	svc.shuttingDown.Store(true)

	_, err := svc.StartBackup(context.Background(), "manual", 14)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shutting down")
}

func TestRecoverStaleRecords(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 模拟一条孤立的 running 记录
	_ = svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "stale-1",
		Status:    "running",
		StartedAt: time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	})
	// 模拟一条孤立的恢复中记录
	_ = svc.saveRecord(context.Background(), &BackupRecord{
		ID:            "stale-2",
		Status:        "completed",
		RestoreStatus: "running",
		StartedAt:     time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	})

	svc.recoverStaleRecords()

	r1, _ := svc.GetBackupRecord(context.Background(), "stale-1")
	require.Equal(t, "failed", r1.Status)
	require.Contains(t, r1.ErrorMsg, "server restart")

	r2, _ := svc.GetBackupRecord(context.Background(), "stale-2")
	require.Equal(t, "failed", r2.RestoreStatus)
	require.Contains(t, r2.RestoreError, "server restart")
}

func TestGracefulShutdown(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &blockingDumper{blockCh: make(chan struct{}), data: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	_, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// Stop 应该等待备份完成
	done := make(chan struct{})
	go func() {
		svc.Stop()
		close(done)
	}()

	// 短暂等待确认 Stop 还在等待
	select {
	case <-done:
		t.Fatal("Stop returned before backup finished")
	case <-time.After(100 * time.Millisecond):
		// 预期：Stop 还在等待
	}

	// 释放备份
	close(dumper.blockCh)

	// 现在 Stop 应该完成
	select {
	case <-done:
		// 预期
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after backup finished")
	}
}

func TestStartRestore_Async(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumpContent := "-- PostgreSQL dump\nCREATE TABLE test (id int);\n"
	dumper := &mockDumper{dumpData: []byte(dumpContent)}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	// 先创建一个备份（同步方式）
	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// 异步恢复
	restored, err := svc.StartRestore(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "running", restored.RestoreStatus)

	svc.wg.Wait()

	// 验证最终状态
	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", final.RestoreStatus)
}

// ─── H2: 备份前缀边界保护测试 ───

func TestNormalizeBackupPrefix(t *testing.T) {
	// normalizeBackupPrefix 现无条件返回 "backups"：Prefix 字段不再可配置，
	// 任何输入（含此前被误接受的 agnes-chat/foo、image-generation/foo、custom-backups 等）
	// 都强制规范化为 "backups"，确保三前缀隔离的硬性边界。
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "默认值", input: "backups", want: "backups"},
		{name: "空值强制默认", input: "", want: "backups"},
		{name: "仅空格强制默认", input: "   ", want: "backups"},
		{name: "点强制默认", input: ".", want: "backups"},
		{name: "双点强制默认", input: "..", want: "backups"},
		{name: "路径穿越强制默认", input: "../etc", want: "backups"},
		{name: "agnes-chat 冲突强制默认", input: "agnes-chat", want: "backups"},
		{name: "image-generation 冲突强制默认", input: "image-generation", want: "backups"},
		{name: "去除首尾斜杠", input: "/backups/", want: "backups"},
		{name: "agnes-chat 子目录强制默认", input: "agnes-chat/archive", want: "backups"},
		{name: "image-generation 子目录强制默认", input: "image-generation/foo", want: "backups"},
		{name: "自定义前缀强制默认", input: "custom-backups", want: "backups"},
		{name: "任意值强制默认", input: "whatever", want: "backups"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, normalizeBackupPrefix(tc.input))
		})
	}
}

func TestAssertBackupKeyPrefix(t *testing.T) {
	require.NoError(t, assertBackupKeyPrefix("backups/2024/01/db.sql.gz"))
	require.NoError(t, assertBackupKeyPrefix("backups/.s3-connection-test/123"))

	// 非 backups/ 前缀应拒绝
	err := assertBackupKeyPrefix("agnes-chat/42/abc.png")
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside backup prefix")

	err = assertBackupKeyPrefix("image-generation/media/images/1/x.png")
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside backup prefix")

	// 空值应拒绝
	err = assertBackupKeyPrefix("")
	require.Error(t, err)

	// 仅 "backups" 不带斜杠应拒绝（避免误删 backups 前缀本身）
	err = assertBackupKeyPrefix("backups")
	require.Error(t, err)
}

// TestBackupService_UpdateS3Config_NormalizesPrefix 验证保存时强制规范化前缀。
func TestBackupService_UpdateS3Config_NormalizesPrefix(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 尝试保存 image-generation 作为备份前缀
	cfg, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:          "b",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
		Prefix:          "image-generation",
	})
	require.NoError(t, err)
	require.Equal(t, "backups", cfg.Prefix, "应强制规范化为 backups")

	// 数据库中存储的也应是 backups
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	var stored BackupS3Config
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, "backups", stored.Prefix)
}

// TestBackupService_UpdateS3Config_PreservesSecretCiphertext 验证编辑非 Secret 字段时
// 保留原始密文，避免解密后以明文写回数据库（H3 修复核心断言）。
func TestBackupService_UpdateS3Config_PreservesSecretCiphertext(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	// 注入 mock 探测器返回 403（CDN 策略生效），避免设置 PublicBaseURL 时触发真实 HTTP 调用
	svc.probeDoer = &mockProbeDoer{status: http.StatusForbidden}

	// 1) 保存带 Secret 的配置
	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:          "my-bucket",
		AccessKeyID:     "AKID",
		SecretAccessKey: "super-secret",
	})
	require.NoError(t, err)

	// 确认数据库中是密文
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	var stored BackupS3Config
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, "ENC:super-secret", stored.SecretAccessKey, "首次保存应为密文")

	// 2) 编辑 PublicBaseURL 但不提供 Secret
	_, err = svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: true,
		// SecretAccessKey 留空
	})
	require.NoError(t, err)

	// 3) 直接断言数据库中仍是密文（ENC: 前缀），未被解密为明文后回写
	// plainEncryptor 仅加 ENC: 前缀，真实加密器密文不会含明文子串；
	// 此处断言字段值仍是密文形态而非明文。
	raw, _ = repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, "ENC:super-secret", stored.SecretAccessKey,
		"编辑非 Secret 字段后数据库中应保留原始密文，不得出现明文")
	require.Equal(t, "https://cdn.example.com", stored.PublicBaseURL)
}

// TestBackupService_LoadS3ConfigRaw_PreservesCiphertext 验证 loadS3ConfigRaw 不解密 Secret。
func TestBackupService_LoadS3ConfigRaw_PreservesCiphertext(t *testing.T) {
	repo := newMockSettingRepo()
	// 直接写入带密文的配置（模拟已加密的数据库状态）
	encrypted := BackupS3Config{
		Bucket:          "b",
		AccessKeyID:     "ak",
		SecretAccessKey: "ENC:my-encrypted-secret",
		Prefix:          "backups",
	}
	data, _ := json.Marshal(encrypted)
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, string(data)))

	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// loadS3ConfigRaw 应返回密文原样
	raw, err := svc.loadS3ConfigRaw(context.Background())
	require.NoError(t, err)
	require.NotNil(t, raw)
	require.Equal(t, "ENC:my-encrypted-secret", raw.SecretAccessKey,
		"loadS3ConfigRaw 不得解密 SecretAccessKey")

	// loadS3Config（解密版）应返回明文
	dec, err := svc.loadS3Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "my-encrypted-secret", dec.SecretAccessKey,
		"loadS3Config 应解密 SecretAccessKey")
}

// TestBackupService_LoadS3ConfigRaw_EmptyReturnsNil 验证未配置时返回 nil。
func TestBackupService_LoadS3ConfigRaw_EmptyReturnsNil(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	raw, err := svc.loadS3ConfigRaw(context.Background())
	require.NoError(t, err)
	require.Nil(t, raw)
}

// TestBackupService_LoadS3ConfigRaw_Corrupted 验证损坏数据返回错误。
func TestBackupService_LoadS3ConfigRaw_Corrupted(t *testing.T) {
	repo := newMockSettingRepo()
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, "not json!!!"))
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	raw, err := svc.loadS3ConfigRaw(context.Background())
	require.Error(t, err)
	require.Nil(t, raw)
}

// TestBackupService_LoadS3ConfigRaw_RepoReadError 验证 settings repository 读取失败时
// 返回错误（而非旧的 (nil,nil) 静默吞错）。这是 H4 修复的核心：GetValue 错误必须传播，
// 仅 raw == "" 才表示不存在旧配置。
func TestBackupService_LoadS3ConfigRaw_RepoReadError(t *testing.T) {
	repo := newMockSettingRepo()
	repo.getValueErr = errors.New("db connection refused")
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	raw, err := svc.loadS3ConfigRaw(context.Background())
	require.Error(t, err, "GetValue 失败时 loadS3ConfigRaw 必须返回错误，不得静默返回 (nil,nil)")
	require.Nil(t, raw)
	require.Contains(t, err.Error(), "read backup s3 config")
}

// TestBackupService_LoadS3Config_RepoReadError 验证 loadS3Config 同样传播 GetValue 错误。
func TestBackupService_LoadS3Config_RepoReadError(t *testing.T) {
	repo := newMockSettingRepo()
	repo.getValueErr = errors.New("db connection refused")
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	cfg, err := svc.loadS3Config(context.Background())
	require.Error(t, err, "GetValue 失败时 loadS3Config 必须返回错误")
	require.Nil(t, cfg)
	require.Contains(t, err.Error(), "read backup s3 config")
}

// TestBackupService_UpdateS3Config_PropagatesLoadRawError 验证当 loadS3ConfigRaw 失败
// （如 JSON 损坏）时，UpdateS3Config 传播错误而非静默保存空 Secret 覆盖既有凭证。
// 这是 H4 修复核心：旧实现 old, _ := loadS3ConfigRaw(ctx) 忽略错误，故障下会丢失 Secret。
func TestBackupService_UpdateS3Config_PropagatesLoadRawError(t *testing.T) {
	repo := newMockSettingRepo()
	// 写入损坏的 JSON 模拟配置损坏
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, "not json!!!"))
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 留空 Secret 修改其他字段：应因 loadS3ConfigRaw 错误而失败，不得保存
	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:      "my-bucket",
		AccessKeyID: "AKID",
		// SecretAccessKey 留空
	})
	require.Error(t, err, "loadS3ConfigRaw 失败时应传播错误，不得静默保存空 Secret")
	require.Contains(t, err.Error(), "load previous s3 config")

	// 损坏的原始数据应未被覆盖（证明未执行保存）
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.Equal(t, "not json!!!", raw, "失败时不得覆盖既有（虽已损坏的）配置")
}

// TestBackupService_UpdateS3Config_PropagatesRepoReadError 验证 settings repository
// 读取失败（如 DB 暂时不可用）时，UpdateS3Config 传播错误而非静默保存空 Secret。
// 这是 H4 场景中 reviewer 重点指出的情况：旧实现将 GetValue 错误与 raw=="" 混为同一分支，
// 导致故障下 Secret 被清空覆盖。此测试直接验证 repository 读取错误路径。
func TestBackupService_UpdateS3Config_PropagatesRepoReadError(t *testing.T) {
	repo := newMockSettingRepo()
	// 先写入一份合法的旧配置（含已加密 Secret），证明旧凭证确实存在
	existing := BackupS3Config{
		Endpoint:        "https://old.r2.com",
		Bucket:          "old-bucket",
		AccessKeyID:     "OLD_AKID",
		SecretAccessKey: "ENC:old-secret",
	}
	existingData, _ := json.Marshal(existing)
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, string(existingData)))

	// 模拟 settings repository 读取失败（如 DB 短暂不可用）
	repo.getValueErr = errors.New("db connection refused")
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 留空 Secret 仅修改 Endpoint：应因 GetValue 错误而失败，不得覆盖既有配置
	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Endpoint:    "https://new.r2.com",
		Bucket:      "old-bucket",
		AccessKeyID: "OLD_AKID",
		// SecretAccessKey 留空 → 期望保留旧密文
	})
	require.Error(t, err, "GetValue 失败时必须传播错误，不得静默保存空 Secret 覆盖既有凭证")
	require.Contains(t, err.Error(), "load previous s3 config")

	// 故障期间不得写入 settings（旧配置应原样保留，含旧密文）
	repo.getValueErr = nil // 恢复读取以验证
	persisted, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.Equal(t, string(existingData), persisted, "故障期间不得覆盖既有配置")
}

// TestBackupService_UpdateS3Config_EmptySecretFirstTimeAllowed 验证首次配置（无旧配置）
// 时空 Secret 仍可保存——old == nil 且无错误时允许空 Secret。
func TestBackupService_UpdateS3Config_EmptySecretFirstTimeAllowed(t *testing.T) {
	repo := newMockSettingRepo() // 空 repo，无任何配置
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 首次配置，留空 Secret：应成功保存（分两步填写的场景）
	cfg, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:      "my-bucket",
		AccessKeyID: "AKID",
		// SecretAccessKey 留空
	})
	require.NoError(t, err)
	require.Equal(t, "", cfg.SecretAccessKey, "首次配置空 Secret 应原样保存")
}

// TestBackupConfigSignature 验证配置指纹计算。
func TestBackupConfigSignature(t *testing.T) {
	// nil 返回空串
	require.Equal(t, "", backupConfigSignature(nil))

	cfg1 := &BackupS3Config{
		Endpoint:        "https://e1.r2.com",
		Region:          "auto",
		Bucket:          "b1",
		AccessKeyID:     "ak1",
		SecretAccessKey: "sk1",
		ForcePathStyle:  false,
	}
	cfg2 := &BackupS3Config{
		Endpoint:        "https://e1.r2.com",
		Region:          "auto",
		Bucket:          "b1",
		AccessKeyID:     "ak1",
		SecretAccessKey: "sk1",
		ForcePathStyle:  false,
	}
	// 相同配置应产生相同指纹
	require.Equal(t, backupConfigSignature(cfg1), backupConfigSignature(cfg2))

	// 任一字段变化应产生不同指纹
	cfg3 := &BackupS3Config{
		Endpoint:        "https://e2.r2.com", // 不同 Endpoint
		Region:          "auto",
		Bucket:          "b1",
		AccessKeyID:     "ak1",
		SecretAccessKey: "sk1",
		ForcePathStyle:  false,
	}
	require.NotEqual(t, backupConfigSignature(cfg1), backupConfigSignature(cfg3))

	// Secret 变化也应产生不同指纹（用于检测凭证轮换）
	cfg4 := &BackupS3Config{
		Endpoint:        "https://e1.r2.com",
		Region:          "auto",
		Bucket:          "b1",
		AccessKeyID:     "ak1",
		SecretAccessKey: "sk2", // 不同 Secret
		ForcePathStyle:  false,
	}
	require.NotEqual(t, backupConfigSignature(cfg1), backupConfigSignature(cfg4))
}

// TestBackupService_GetOrCreateStore_RebuildsOnConfigChange 验证配置指纹变化时重建 S3 客户端（M1）。
func TestBackupService_GetOrCreateStore_RebuildsOnConfigChange(t *testing.T) {
	repo := newMockSettingRepo()
	factoryCalls := 0
	store1 := newMockObjectStore()
	store2 := newMockObjectStore()
	currentStore := store1
	cfg := &config.Config{Database: config.DatabaseConfig{}}
	factory := func(_ context.Context, _ *BackupS3Config) (BackupObjectStore, error) {
		factoryCalls++
		return currentStore, nil
	}
	svc := NewBackupService(repo, cfg, &plainEncryptor{}, factory, &mockDumper{})

	// 首次调用：创建 store
	cfg1 := &BackupS3Config{Bucket: "b1", AccessKeyID: "ak1", SecretAccessKey: "sk1"}
	got, err := svc.getOrCreateStore(context.Background(), cfg1)
	require.NoError(t, err)
	require.Equal(t, 1, factoryCalls)
	require.Same(t, store1, got)

	// 相同配置：复用缓存
	got, err = svc.getOrCreateStore(context.Background(), cfg1)
	require.NoError(t, err)
	require.Equal(t, 1, factoryCalls, "配置未变应复用缓存")
	require.Same(t, store1, got)

	// 切换到新 store 并改变配置
	currentStore = store2
	cfg2 := &BackupS3Config{Bucket: "b2", AccessKeyID: "ak2", SecretAccessKey: "sk2"}
	got, err = svc.getOrCreateStore(context.Background(), cfg2)
	require.NoError(t, err)
	require.Equal(t, 2, factoryCalls, "配置变化应重建客户端")
	require.Same(t, store2, got)
}

// TestBackupService_DeleteBackup_SkipsCrossPrefixS3Delete 验证删除记录时若 s3_key 跨前缀，
// S3 对象不会被删除（H2 安全边界）。DB 记录仍会清理以避免永久残留。
func TestBackupService_DeleteBackup_SkipsCrossPrefixS3Delete(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)

	// 模拟损坏的备份记录：s3_key 指向 agnes-chat 命名空间
	dangerousKey := "agnes-chat/42/abc.png"
	// 预置该对象存在（模拟 S3 中确实有此对象，但属于图片命名空间）
	store.mu.Lock()
	store.objects[dangerousKey] = []byte("image-data")
	store.mu.Unlock()

	record := &BackupRecord{
		ID:         "bad-rec",
		Status:     "completed",
		BackupType: "postgres",
		FileName:   "fake.sql.gz",
		S3Key:      dangerousKey,
		StartedAt:  time.Now().Format(time.RFC3339),
	}
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupRecords, mustJSON(t, []*BackupRecord{record})))

	// DeleteBackup 不返回错误（DB 记录清理成功），但 S3 对象必须保留
	err := svc.DeleteBackup(context.Background(), "bad-rec")
	require.NoError(t, err, "DeleteBackup 应清理 DB 记录，不因跨前缀 S3 key 报错")

	// 关键断言：跨前缀的 S3 对象绝不能被删除
	store.mu.Lock()
	_, exists := store.objects[dangerousKey]
	store.mu.Unlock()
	require.True(t, exists, "跨前缀 S3 对象必须保留，不得被 DeleteBackup 删除")

	// DB 记录应已被清理
	records, err := svc.loadRecords(context.Background())
	require.NoError(t, err)
	require.Empty(t, records, "DB 记录应已被清理")
}

// TestBackupService_DeleteS3Object_RejectsCrossPrefix 验证底层删除也校验前缀。
func TestBackupService_DeleteS3Object_RejectsCrossPrefix(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)

	// 预置一个对象
	store.objects["agnes-chat/x.png"] = []byte("data")

	err := svc.deleteS3Object(context.Background(), "agnes-chat/x.png")
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside backup prefix")
}

// TestBackupService_BuildS3Key_AlwaysBackupsPrefix 验证生成的 key 始终以 backups/ 开头，
// 且包含随机段使路径不可预测（PublicBaseURL 公开桶场景下的纵深防御）。
func TestBackupService_BuildS3Key_AlwaysBackupsPrefix(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())

	cases := []struct {
		name   string
		prefix string
	}{
		{name: "空值", prefix: ""},
		{name: "image-generation", prefix: "image-generation"},
		{name: "agnes-chat", prefix: "agnes-chat"},
		{name: "路径穿越", prefix: "../etc"},
		{name: "合法 backups", prefix: "backups"},
		{name: "agnes-chat 子目录", prefix: "agnes-chat/archive"},
		{name: "自定义前缀", prefix: "custom-backups"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &BackupS3Config{Prefix: tc.prefix}
			key := svc.buildS3Key(cfg, "db.sql.gz")
			require.True(t, strings.HasPrefix(key, "backups/"),
				"buildS3Key 必须以 backups/ 开头，got: %s", key)
			// 结构：backups/{yyyy}/{mm}/{dd}/{uuid}/db.sql.gz
			parts := strings.Split(key, "/")
			require.Len(t, parts, 6, "key 应包含 6 段：backups/yyyy/mm/dd/uuid/filename，got: %s", key)
			require.NotEmpty(t, parts[4], "随机段不得为空，got: %s", key)
			require.Equal(t, "db.sql.gz", parts[5], "最后一段应为文件名")
			// 两次生成 key 不同（随机段不同）
			key2 := svc.buildS3Key(cfg, "db.sql.gz")
			require.NotEqual(t, key, key2, "两次生成的 key 应不同（随机段）")
		})
	}
}

// ─── H1: PublicBaseURL backups/ 公开性探测 ───

// TestVerifyBackupPrefixNotPublic_AllowableResponses 验证探测对各状态码的判定。
// 探测使用 GET + Range（而非 HEAD），响应判定为 fail-closed。
func TestVerifyBackupPrefixNotPublic_AllowableResponses(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		wantErr   bool
		wantInErr string
	}{
		{name: "403 表示策略生效", status: http.StatusForbidden, wantErr: false},
		{name: "401 表示需鉴权", status: http.StatusUnauthorized, wantErr: false},
		{name: "404 表示 backups/ 公开可读", status: http.StatusNotFound, wantErr: true, wantInErr: "BACKUP_PREFIX_PUBLICLY_READABLE"},
		{name: "200 表示内容被返回（危险）", status: http.StatusOK, wantErr: true, wantInErr: "content served"},
		{name: "206 partial content 表示内容被返回（危险）", status: http.StatusPartialContent, wantErr: true, wantInErr: "content served"},
		{name: "302 重定向视为危险（fail-closed）", status: http.StatusFound, wantErr: true, wantInErr: "unexpected status 302"},
		{name: "500 服务器错误视为危险（fail-closed）", status: http.StatusInternalServerError, wantErr: true, wantInErr: "unexpected status 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
			svc.probeDoer = &mockProbeDoer{status: tc.status}
			err := svc.verifyBackupPrefixNotPublic(context.Background(), "https://cdn.example.com")
			if tc.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantInErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestVerifyBackupPrefixNotPublic_NetworkErrorIsFailClosed 验证网络不可达时
// 探测 fail-closed 拒绝保存（旧实现 warn-only 会允许公开 bucket 配置保存）。
func TestVerifyBackupPrefixNotPublic_NetworkErrorIsFailClosed(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
	svc.probeDoer = &mockProbeDoer{err: errors.New("dial tcp: no such host")}
	err := svc.verifyBackupPrefixNotPublic(context.Background(), "https://cdn.example.com")
	require.Error(t, err, "网络不可达应 fail-closed 拒绝保存，不得放行未验证的公开配置")
	require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)
	require.Contains(t, err.Error(), "cannot reach PublicBaseURL")
}

// TestVerifyBackupPrefixNotPublic_EmptyBaseSkipsProbe 验证未配置 PublicBaseURL 时不探测。
func TestVerifyBackupPrefixNotPublic_EmptyBaseSkipsProbe(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
	doer := &mockProbeDoer{status: http.StatusNotFound}
	svc.probeDoer = doer
	require.NoError(t, svc.verifyBackupPrefixNotPublic(context.Background(), ""))
	require.Empty(t, doer.lastURL, "空 PublicBaseURL 不应发起探测")
}

// TestVerifyBackupPrefixNotPublic_ProbeURLFormat 验证探测 URL 格式与方法：
// {PublicBaseURL}/backups/.policy-probe-<uuid>，GET 方法 + Range: bytes=0-0。
func TestVerifyBackupPrefixNotPublic_ProbeURLFormat(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
	doer := &mockProbeDoer{status: http.StatusForbidden}
	svc.probeDoer = doer
	require.NoError(t, svc.verifyBackupPrefixNotPublic(context.Background(), "https://cdn.example.com/"))
	require.Contains(t, doer.lastURL, "https://cdn.example.com/backups/.policy-probe-",
		"探测 URL 应为 {base}/backups/.policy-probe-<uuid>")
	require.Equal(t, http.MethodGet, doer.lastMethod, "探测应使用 GET 而非 HEAD（CDN 可能仅拒绝 HEAD）")
	require.Equal(t, "bytes=0-0", doer.lastRange, "探测应带 Range: bytes=0-0 限制响应体")
}

// ─── SSRF 防护测试 ───

// TestVerifyBackupPrefixNotPublic_SSRF_BlocksNonHTTPS 验证 http:// URL 被拒绝。
func TestVerifyBackupPrefixNotPublic_SSRF_BlocksNonHTTPS(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
	doer := &mockProbeDoer{status: http.StatusForbidden}
	svc.probeDoer = doer
	err := svc.verifyBackupPrefixNotPublic(context.Background(), "http://cdn.example.com")
	require.Error(t, err, "http:// URL 应被 SSRF 防护拒绝")
	require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)
	require.Empty(t, doer.lastURL, "SSRF 拒绝时不应发起 HTTP 请求")
}

// TestVerifyBackupPrefixNotPublic_SSRF_BlocksLocalhost 验证 localhost 被拒绝。
func TestVerifyBackupPrefixNotPublic_SSRF_BlocksLocalhost(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
	doer := &mockProbeDoer{status: http.StatusForbidden}
	svc.probeDoer = doer
	for _, u := range []string{
		"https://localhost",
		"https://localhost:8443",
		"https://127.0.0.1",
		"https://127.0.0.1:8080",
	} {
		err := svc.verifyBackupPrefixNotPublic(context.Background(), u)
		require.Error(t, err, "%s 应被 SSRF 防护拒绝", u)
		require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)
	}
	require.Empty(t, doer.lastURL, "SSRF 拒绝时不应发起 HTTP 请求")
}

// TestVerifyBackupPrefixNotPublic_SSRF_BlocksPrivateIP 验证私网 IP 字面量被拒绝。
func TestVerifyBackupPrefixNotPublic_SSRF_BlocksPrivateIP(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
	doer := &mockProbeDoer{status: http.StatusForbidden}
	svc.probeDoer = doer
	for _, u := range []string{
		"https://10.0.0.1",
		"https://172.16.0.1",
		"https://192.168.1.1",
		"https://169.254.169.254", // 云元数据
	} {
		err := svc.verifyBackupPrefixNotPublic(context.Background(), u)
		require.Error(t, err, "%s 应被 SSRF 防护拒绝", u)
		require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)
	}
	require.Empty(t, doer.lastURL, "SSRF 拒绝时不应发起 HTTP 请求")
}

// TestVerifyBackupPrefixNotPublic_SSRF_BlocksCloudMetadataHostname 验证云元数据 hostname 被拒绝。
func TestVerifyBackupPrefixNotPublic_SSRF_BlocksCloudMetadataHostname(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
	doer := &mockProbeDoer{status: http.StatusForbidden}
	svc.probeDoer = doer
	for _, u := range []string{
		"https://metadata.google.internal",
		"https://metadata.goog",
		"https://instance-data.ec2.internal",
	} {
		err := svc.verifyBackupPrefixNotPublic(context.Background(), u)
		require.Error(t, err, "%s 应被 SSRF 防护拒绝", u)
		require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)
	}
	require.Empty(t, doer.lastURL, "SSRF 拒绝时不应发起 HTTP 请求")
}

// TestVerifyBackupPrefixNotPublic_SSRF_BlocksInvalidURL 验证格式错误的 URL 被拒绝。
func TestVerifyBackupPrefixNotPublic_SSRF_BlocksInvalidURL(t *testing.T) {
	svc := newTestBackupService(newMockSettingRepo(), &mockDumper{}, newMockObjectStore())
	doer := &mockProbeDoer{status: http.StatusForbidden}
	svc.probeDoer = doer
	for _, u := range []string{
		"not-a-url",
		"ftp://cdn.example.com",
		"://missing-scheme",
	} {
		err := svc.verifyBackupPrefixNotPublic(context.Background(), u)
		require.Error(t, err, "%q 应被拒绝", u)
		require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)
	}
	require.Empty(t, doer.lastURL, "SSRF 拒绝时不应发起 HTTP 请求")
}

// TestUpdateS3Config_PublicBackupsRefusesSave 验证当探测发现 backups/ 公开可读（404）时，
// UpdateS3Config 拒绝保存（fail-closed），既有配置不被覆盖。
func TestUpdateS3Config_PublicBackupsRefusesSave(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	// 探测返回 404 → backups/ 公开可读
	svc.probeDoer = &mockProbeDoer{status: http.StatusNotFound}

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: true,
	})
	require.Error(t, err, "backups/ 公开可读时应拒绝保存配置")
	require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)

	// 配置不应被写入（settings 仍为空）
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.Empty(t, raw, "拒绝保存时不得写入 settings")
}

// TestUpdateS3Config_SafeCDNAllowsSave 验证探测返回 403（CDN 策略生效）时，
// UpdateS3Config 正常保存配置。
func TestUpdateS3Config_SafeCDNAllowsSave(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	svc.probeDoer = &mockProbeDoer{status: http.StatusForbidden}

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: true,
	})
	require.NoError(t, err)
	// 配置应被写入
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.NotEmpty(t, raw, "CDN 策略生效时应正常保存")
}

// TestUpdateS3Config_NetworkUnreachableRefusesSave 验证 CDN 不可达时 fail-closed 拒绝保存。
func TestUpdateS3Config_NetworkUnreachableRefusesSave(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	svc.probeDoer = &mockProbeDoer{err: errors.New("dial tcp: lookup cdn.example.com: no such host")}

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: true,
	})
	require.Error(t, err, "CDN 不可达时应 fail-closed 拒绝保存")
	require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)

	// 配置不应被写入
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.Empty(t, raw, "拒绝保存时不得写入 settings")
}

// TestUpdateS3Config_SSRFURLRefusesSave 验证 SSRF URL（私网/localhost）被拒绝保存。
func TestUpdateS3Config_SSRFURLRefusesSave(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	svc.probeDoer = &mockProbeDoer{status: http.StatusForbidden} // 即使探测会放行，SSRF 校验应先拒绝

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "http://169.254.169.254", // SSRF: 云元数据 + http
		BucketPrivacyAttested: true,
	})
	require.Error(t, err, "SSRF URL 应被拒绝保存")
	require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)

	// 配置不应被写入
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.Empty(t, raw, "SSRF 拒绝时不得写入 settings")
}

// ─── 多公开入口探测测试 ───

// TestUpdateS3Config_ProbesAllPublicEntryPoints 验证 UpdateS3Config 探测
// PublicBaseURL + AdditionalPublicBaseURLs 的每一个入口，任一暴露 backups/ 即拒绝保存。
// 这是本 issue 修复的核心：Worker 仅保护 PublicBaseURL，r2.dev 等其他入口可绕过。
func TestUpdateS3Config_ProbesAllPublicEntryPoints(t *testing.T) {
	cases := []struct {
		name      string
		byHost    map[string]int   // host → 探测响应
		errs      map[string]error // host → 网络错误
		wantErr   bool
		wantInErr string
	}{
		{
			name: "所有入口都拒绝 backups/（403）→ 允许保存",
			byHost: map[string]int{
				"cdn.example.com":  http.StatusForbidden,
				"pub-abc.r2.dev":   http.StatusForbidden,
				"cdn2.example.com": http.StatusForbidden,
			},
			wantErr: false,
		},
		{
			name: "r2.dev 入口返回 404（公开可读）→ 拒绝保存",
			byHost: map[string]int{
				"cdn.example.com": http.StatusForbidden,
				"pub-abc.r2.dev":  http.StatusNotFound,
			},
			wantErr:   true,
			wantInErr: "pub-abc.r2.dev",
		},
		{
			name: "第二个 custom domain 返回 200（内容被返回）→ 拒绝保存",
			byHost: map[string]int{
				"cdn.example.com":  http.StatusForbidden,
				"cdn2.example.com": http.StatusOK,
			},
			wantErr:   true,
			wantInErr: "cdn2.example.com",
		},
		{
			name: "r2.dev 入口不可达 → fail-closed 拒绝保存",
			errs: map[string]error{
				"pub-abc.r2.dev": errors.New("dial tcp: connection refused"),
			},
			byHost: map[string]int{
				"cdn.example.com": http.StatusForbidden,
			},
			wantErr:   true,
			wantInErr: "pub-abc.r2.dev",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockSettingRepo()
			svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
			svc.probeDoer = &perHostMockProbeDoer{byHost: tc.byHost, errs: tc.errs}

			_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
				Bucket:                "my-bucket",
				AccessKeyID:           "AKID",
				SecretAccessKey:       "sk",
				PublicBaseURL:         "https://cdn.example.com",
				BucketPrivacyAttested: true,
				AdditionalPublicBaseURLs: []string{
					"https://pub-abc.r2.dev",
					"https://cdn2.example.com",
				},
			})
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)
				require.Contains(t, err.Error(), tc.wantInErr, "错误信息应指出哪个入口失败")
				// 配置不应被写入
				raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
				require.Empty(t, raw, "任一入口失败时不得写入 settings")
			} else {
				require.NoError(t, err)
				raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
				require.NotEmpty(t, raw, "所有入口都安全时应正常保存")
			}
		})
	}
}

// TestUpdateS3Config_AdditionalURLsSSRFRejected 验证 AdditionalPublicBaseURLs 中的
// SSRF URL（私网/localhost/http）同样被拒绝。
func TestUpdateS3Config_AdditionalURLsSSRFRejected(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	svc.probeDoer = &mockProbeDoer{status: http.StatusForbidden}

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: true,
		AdditionalPublicBaseURLs: []string{
			"http://169.254.169.254", // SSRF: 云元数据 + http
		},
	})
	require.Error(t, err, "AdditionalPublicBaseURLs 中的 SSRF URL 应被拒绝")
	require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)

	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.Empty(t, raw, "SSRF 拒绝时不得写入 settings")
}

// TestUpdateS3Config_EmptyAdditionalURLsSkipsProbe 验证 AdditionalPublicBaseURLs 为空时
// 只探测 PublicBaseURL（兼容旧配置）。
func TestUpdateS3Config_EmptyAdditionalURLsSkipsProbe(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	doer := &mockProbeDoer{status: http.StatusForbidden}
	svc.probeDoer = doer

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: true,
		// AdditionalPublicBaseURLs 留空
	})
	require.NoError(t, err)
	require.Contains(t, doer.lastURL, "cdn.example.com", "应探测 PublicBaseURL")
}

// TestTestS3Connection_ProbesAllPublicEntryPoints 验证 TestS3Connection 也探测所有公开入口。
func TestTestS3Connection_ProbesAllPublicEntryPoints(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	svc.probeDoer = &perHostMockProbeDoer{
		byHost: map[string]int{
			"cdn.example.com": http.StatusForbidden,
			"pub-abc.r2.dev":  http.StatusNotFound, // r2.dev 暴露 backups/
		},
	}

	err := svc.TestS3Connection(context.Background(), BackupS3Config{
		Bucket:                "test",
		AccessKeyID:           "ak",
		SecretAccessKey:       "sk",
		Prefix:                "backups",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: true,
		AdditionalPublicBaseURLs: []string{
			"https://pub-abc.r2.dev",
		},
	})
	require.Error(t, err, "r2.dev 暴露 backups/ 时测试连接应失败")
	require.ErrorIs(t, err, ErrBackupPrefixPubliclyReadable)
	require.Contains(t, err.Error(), "pub-abc.r2.dev")
}

// ─── BucketPrivacyAttested 强制校验测试 ───
//
// 这一组测试对应"公开入口靠管理员声明，无法保证 bucket 级私有"的修复：
// PublicBaseURL 非空时，UpdateS3Config / TestS3Connection 必须强制要求
// BucketPrivacyAttested=true，否则拒绝保存/测试。
// attestation 是 HARD 前提，弥补声明式 AdditionalPublicBaseURLs 可能漏报的盲区。

// TestUpdateS3Config_RejectsPublicBaseURLWithoutAttestation 验证 PublicBaseURL 非空但
// 管理员未勾选 attestation 时，UpdateS3Config 直接拒绝保存（不进入探测阶段）。
func TestUpdateS3Config_RejectsPublicBaseURLWithoutAttestation(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	// 即使探测器会放行（403），attestation 缺失应先于探测拒绝
	svc.probeDoer = &mockProbeDoer{status: http.StatusForbidden}

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: false, // 未勾选承诺
	})
	require.Error(t, err, "PublicBaseURL 非空但未 attestation 时必须拒绝保存")
	require.ErrorIs(t, err, ErrBucketPrivacyNotAttested)

	// 配置不应被写入
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.Empty(t, raw, "attestation 缺失时不得写入 settings")
}

// TestUpdateS3Config_AttestationNotRequiredWhenPublicBaseURLEmpty 验证 PublicBaseURL 留空
// （仅使用 presigned URL，bucket 完全私有）时不需要 attestation。
func TestUpdateS3Config_AttestationNotRequiredWhenPublicBaseURLEmpty(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "", // 留空 = 不公开暴露
		BucketPrivacyAttested: false,
	})
	require.NoError(t, err, "PublicBaseURL 留空时不应要求 attestation")

	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.NotEmpty(t, raw, "无公开入口时应正常保存")
}

// TestUpdateS3Config_AttestationShortCircuitsBeforeProbe 验证 attestation 校验先于探测执行，
// 即使探测器会返回"公开可读"（404），attestation 缺失时也应返回 ErrBucketPrivacyNotAttested
// 而非 ErrBackupPrefixPubliclyReadable。这确保错误信息准确指向"未承诺"而非"探测失败"。
func TestUpdateS3Config_AttestationShortCircuitsBeforeProbe(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	// 探测器返回 404（公开可读）——若执行到探测阶段会返回 ErrBackupPrefixPubliclyReadable
	svc.probeDoer = &mockProbeDoer{status: http.StatusNotFound}

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:                "my-bucket",
		AccessKeyID:           "AKID",
		SecretAccessKey:       "sk",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: false,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBucketPrivacyNotAttested,
		"attestation 缺失应先于探测拒绝，错误码必须是 BUCKET_PRIVACY_NOT_ATTESTED")
	require.NotErrorIs(t, err, ErrBackupPrefixPubliclyReadable,
		"attestation 缺失时不应进入探测阶段")
}

// TestTestS3Connection_RejectsPublicBaseURLWithoutAttestation 验证 TestS3Connection 同样
// 在 PublicBaseURL 非空但未 attestation 时拒绝测试。
func TestTestS3Connection_RejectsPublicBaseURLWithoutAttestation(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	svc.probeDoer = &mockProbeDoer{status: http.StatusForbidden}

	err := svc.TestS3Connection(context.Background(), BackupS3Config{
		Bucket:                "test",
		AccessKeyID:           "ak",
		SecretAccessKey:       "sk",
		Prefix:                "backups",
		PublicBaseURL:         "https://cdn.example.com",
		BucketPrivacyAttested: false,
	})
	require.Error(t, err, "TestS3Connection 也应在 attestation 缺失时拒绝")
	require.ErrorIs(t, err, ErrBucketPrivacyNotAttested)

	// 测试对象不应残留（attestation 在 S3 操作之后，但测试对象在步骤 4 已被删除；
	// 此处额外验证不会因 attestation 失败而影响 store 状态）
	store.mu.Lock()
	objCount := len(store.objects)
	store.mu.Unlock()
	require.Zero(t, objCount, "attestation 失败不应残留测试对象")
}
