//go:build unit

package repository

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// ---- Mocks ----

// fakeConfigReader 模拟 SharedObjectStorageConfigReader。
type fakeConfigReader struct {
	cfg *service.BackupS3Config
	err error
}

func (f *fakeConfigReader) GetSharedObjectStorageConfig(_ context.Context) (*service.BackupS3Config, error) {
	return f.cfg, f.err
}

// fakeStorage 是 EnovaImageAssetStorage 的可控 mock。
type fakeStorage struct {
	putCalls      []service.PutObjectInput
	deleteCalls   []string
	presignCalls  []string
	configuredVal bool
	putErr        error
}

func (s *fakeStorage) Put(_ context.Context, input service.PutObjectInput) (*service.StoredObject, error) {
	if s.putErr != nil {
		return nil, s.putErr
	}
	s.putCalls = append(s.putCalls, input)
	body, _ := io.ReadAll(input.Body)
	return &service.StoredObject{Bucket: "fake-bucket", Key: input.Key, Size: int64(len(body)), MimeType: input.ContentType}, nil
}
func (s *fakeStorage) Delete(_ context.Context, key string) error {
	s.deleteCalls = append(s.deleteCalls, key)
	return nil
}
func (s *fakeStorage) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	s.presignCalls = append(s.presignCalls, key)
	return "https://presigned.example.com/" + key, nil
}
func (s *fakeStorage) PresignPut(_ context.Context, key string, _ string, _ time.Duration) (string, error) {
	return "https://presigned.example.com/put/" + key, nil
}
func (s *fakeStorage) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStorage) Head(_ context.Context, _ string) (*service.ObjectHead, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStorage) Bucket() string   { return "fake-bucket" }
func (s *fakeStorage) Configured() bool { return s.configuredVal }
func (s *fakeStorage) Driver() string   { return "s3" }

// fakeFactory 返回固定的 fakeStorage 实例，便于断言调用次数。
func fakeFactory(stored *fakeStorage) service.EnovaImageAssetStorageFactory {
	return func(cfg service.EnovaImageAssetStorageConfig) (service.EnovaImageAssetStorage, error) {
		// 校验传入的配置：Agnes 必须使用固定前缀 agnes-chat
		if cfg.Prefix != service.AgnesChatPrefix {
			return nil, errors.New("expected prefix " + service.AgnesChatPrefix + ", got " + cfg.Prefix)
		}
		return stored, nil
	}
}

// ---- Tests ----

// TestLazyAgnesChatStorage_NotConfiguredReturnsError 验证未配置时返回 ErrStorageNotConfigured。
func TestLazyAgnesChatStorage_NotConfiguredReturnsError(t *testing.T) {
	reader := &fakeConfigReader{cfg: nil}
	storage := &fakeStorage{configuredVal: false}
	adapter, err := ProvideAgnesChatImageStorage(reader, fakeFactory(storage))
	require.NoError(t, err)

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "test-key",
		Body:        stringReader("hello"),
		ContentType: "image/png",
	})
	require.ErrorIs(t, err, service.ErrStorageNotConfigured)
}

// TestLazyAgnesChatStorage_IncompleteConfigReturnsError 验证字段不完整时返回错误。
func TestLazyAgnesChatStorage_IncompleteConfigReturnsError(t *testing.T) {
	reader := &fakeConfigReader{cfg: &service.BackupS3Config{Bucket: "only-bucket"}}
	storage := &fakeStorage{configuredVal: false}
	adapter, err := ProvideAgnesChatImageStorage(reader, fakeFactory(storage))
	require.NoError(t, err)

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "test-key",
		Body:        stringReader("hello"),
		ContentType: "image/png",
	})
	require.ErrorIs(t, err, service.ErrStorageNotConfigured)
}

// TestLazyAgnesChatStorage_ReaderErrorPropagates 验证配置读取失败时错误传播。
func TestLazyAgnesChatStorage_ReaderErrorPropagates(t *testing.T) {
	readerErr := errors.New("db connection lost")
	reader := &fakeConfigReader{err: readerErr}
	storage := &fakeStorage{}
	adapter, err := ProvideAgnesChatImageStorage(reader, fakeFactory(storage))
	require.NoError(t, err)

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "test-key",
		Body:        stringReader("hello"),
		ContentType: "image/png",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "read shared object storage config")
}

// TestLazyAgnesChatStorage_PutUsesAgnesChatPrefix 验证 Agnes 使用固定前缀 agnes-chat。
// 这是统一存储配置后的关键隔离机制：Agnes 图片和数据库备份使用不同前缀。
func TestLazyAgnesChatStorage_PutUsesAgnesChatPrefix(t *testing.T) {
	reader := &fakeConfigReader{cfg: &service.BackupS3Config{
		Endpoint:        "https://r2.example.com",
		Region:          "auto",
		Bucket:          "shared-bucket",
		AccessKeyID:     "akid",
		SecretAccessKey: "secret",
		Prefix:          "backups", // 备份前缀，Agnes 应忽略此字段
		PublicBaseURL:   "https://cdn.example.com",
	}}
	storage := &fakeStorage{configuredVal: true}
	adapter, err := ProvideAgnesChatImageStorage(reader, fakeFactory(storage))
	require.NoError(t, err)

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "42/abc123.png",
		Body:        stringReader("png-data"),
		ContentType: "image/png",
	})
	require.NoError(t, err)
	require.Len(t, storage.putCalls, 1)
	// factory 内部已校验 cfg.Prefix == AgnesChatPrefix
}

// TestLazyAgnesChatStorage_ConfiguredReflectsDBConfig 验证 Configured() 反映数据库配置状态。
func TestLazyAgnesChatStorage_ConfiguredReflectsDBConfig(t *testing.T) {
	// 未配置
	adapter, err := ProvideAgnesChatImageStorage(
		&fakeConfigReader{cfg: nil},
		fakeFactory(&fakeStorage{}),
	)
	require.NoError(t, err)
	require.False(t, adapter.Configured())

	// 已配置完整
	adapter2, err := ProvideAgnesChatImageStorage(
		&fakeConfigReader{cfg: &service.BackupS3Config{
			Bucket:          "b",
			AccessKeyID:     "ak",
			SecretAccessKey: "sk",
		}},
		fakeFactory(&fakeStorage{}),
	)
	require.NoError(t, err)
	require.True(t, adapter2.Configured())
}

// TestLazyAgnesChatStorage_NilReaderReturnsNotConfigured 验证 reader 为 nil 时不 panic。
func TestLazyAgnesChatStorage_NilReaderReturnsNotConfigured(t *testing.T) {
	adapter, err := ProvideAgnesChatImageStorage(nil, fakeFactory(&fakeStorage{}))
	require.NoError(t, err)
	require.False(t, adapter.Configured())

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "test",
		Body:        stringReader("x"),
		ContentType: "image/png",
	})
	require.ErrorIs(t, err, service.ErrStorageNotConfigured)
}

// TestLazyAgnesChatStorage_CachesByConfigSignature 验证配置签名不变时复用缓存的 S3 客户端。
func TestLazyAgnesChatStorage_CachesByConfigSignature(t *testing.T) {
	cfg := &service.BackupS3Config{
		Bucket:          "b",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
	}
	reader := &fakeConfigReader{cfg: cfg}
	calls := 0
	factory := func(c service.EnovaImageAssetStorageConfig) (service.EnovaImageAssetStorage, error) {
		calls++
		return &fakeStorage{configuredVal: true}, nil
	}
	adapter, err := ProvideAgnesChatImageStorage(reader, factory)
	require.NoError(t, err)

	// 第一次调用：应触发 factory
	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key: "k1", Body: stringReader("v1"), ContentType: "image/png",
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)

	// 第二次调用（配置未变）：应复用缓存，不触发 factory
	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key: "k2", Body: stringReader("v2"), ContentType: "image/png",
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls, "factory should not be called again when config signature unchanged")
}

// ---- Helpers ----

func stringReader(s string) io.Reader {
	return &stringReaderImpl{s: s}
}

type stringReaderImpl struct {
	s   string
	pos int
}

func (r *stringReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}
