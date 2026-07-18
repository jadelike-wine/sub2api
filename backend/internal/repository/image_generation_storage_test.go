//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 注意：fakeConfigReader / fakeStorage / fakeFactory / stringReader 已在
// agnes_chat_storage_test.go 中定义，Go 同包内复用，不重复声明。

// ---- Tests ----

// TestLazyImageGenerationStorage_NotConfiguredReturnsError 验证未配置时返回 ErrStorageNotConfigured。
func TestLazyImageGenerationStorage_NotConfiguredReturnsError(t *testing.T) {
	reader := &fakeConfigReader{cfg: nil}
	storage := &fakeStorage{configuredVal: false}
	adapter, err := ProvideImageGenerationStorage(reader, fakeFactory(storage))
	require.NoError(t, err)

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "test-key",
		Body:        stringReader("hello"),
		ContentType: "image/png",
	})
	require.ErrorIs(t, err, service.ErrStorageNotConfigured)
}

// TestLazyImageGenerationStorage_IncompleteConfigReturnsError 验证字段不完整时返回错误。
func TestLazyImageGenerationStorage_IncompleteConfigReturnsError(t *testing.T) {
	reader := &fakeConfigReader{cfg: &service.BackupS3Config{Bucket: "only-bucket"}}
	storage := &fakeStorage{configuredVal: false}
	adapter, err := ProvideImageGenerationStorage(reader, fakeFactory(storage))
	require.NoError(t, err)

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "test-key",
		Body:        stringReader("hello"),
		ContentType: "image/png",
	})
	require.ErrorIs(t, err, service.ErrStorageNotConfigured)
}

// TestLazyImageGenerationStorage_ReaderErrorPropagates 验证配置读取失败时错误传播。
func TestLazyImageGenerationStorage_ReaderErrorPropagates(t *testing.T) {
	readerErr := errors.New("db connection lost")
	reader := &fakeConfigReader{err: readerErr}
	storage := &fakeStorage{}
	adapter, err := ProvideImageGenerationStorage(reader, fakeFactory(storage))
	require.NoError(t, err)

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "test-key",
		Body:        stringReader("hello"),
		ContentType: "image/png",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "read shared object storage config")
}

// TestLazyImageGenerationStorage_PutUsesImageGenerationPrefix 验证 AI 生图使用固定前缀 image-generation。
// 这是统一存储配置后的关键隔离机制：AI 生图、Agnes 聊天图片、数据库备份使用不同前缀。
func TestLazyImageGenerationStorage_PutUsesImageGenerationPrefix(t *testing.T) {
	reader := &fakeConfigReader{cfg: &service.BackupS3Config{
		Endpoint:        "https://r2.example.com",
		Region:          "auto",
		Bucket:          "shared-bucket",
		AccessKeyID:     "akid",
		SecretAccessKey: "secret",
		Prefix:          "backups", // 备份前缀，AI 生图应忽略此字段
		PublicBaseURL:   "https://cdn.example.com",
	}}
	storage := &fakeStorage{configuredVal: true}
	// 使用内联 factory 校验 prefix == ImageGenerationPrefix（不与 agnes-chat 的 fakeFactory 冲突）
	factory := func(c service.EnovaImageAssetStorageConfig) (service.EnovaImageAssetStorage, error) {
		require.Equal(t, service.ImageGenerationPrefix, c.Prefix, "AI 生图应使用固定前缀 image-generation")
		return storage, nil
	}
	adapter, err := ProvideImageGenerationStorage(reader, factory)
	require.NoError(t, err)

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "42/abc123.png",
		Body:        stringReader("png-data"),
		ContentType: "image/png",
	})
	require.NoError(t, err)
	require.Len(t, storage.putCalls, 1)
}

// TestLazyImageGenerationStorage_ConfiguredReflectsDBConfig 验证 Configured() 反映数据库配置状态。
func TestLazyImageGenerationStorage_ConfiguredReflectsDBConfig(t *testing.T) {
	// 未配置
	adapter, err := ProvideImageGenerationStorage(
		&fakeConfigReader{cfg: nil},
		fakeFactory(&fakeStorage{}),
	)
	require.NoError(t, err)
	require.False(t, adapter.Configured())

	// 已配置完整
	adapter2, err := ProvideImageGenerationStorage(
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

// TestLazyImageGenerationStorage_NilReaderReturnsNotConfigured 验证 reader 为 nil 时不 panic。
func TestLazyImageGenerationStorage_NilReaderReturnsNotConfigured(t *testing.T) {
	adapter, err := ProvideImageGenerationStorage(nil, fakeFactory(&fakeStorage{}))
	require.NoError(t, err)
	require.False(t, adapter.Configured())

	_, err = adapter.Put(context.Background(), service.PutObjectInput{
		Key:         "test",
		Body:        stringReader("x"),
		ContentType: "image/png",
	})
	require.ErrorIs(t, err, service.ErrStorageNotConfigured)
}

// TestLazyImageGenerationStorage_CachesByConfigSignature 验证配置签名不变时复用缓存的 S3 客户端。
func TestLazyImageGenerationStorage_CachesByConfigSignature(t *testing.T) {
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
	adapter, err := ProvideImageGenerationStorage(reader, factory)
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

// TestLazyImageGenerationStorage_DriverIsS3 验证 Driver() 返回 "s3"。
func TestLazyImageGenerationStorage_DriverIsS3(t *testing.T) {
	adapter, err := ProvideImageGenerationStorage(
		&fakeConfigReader{cfg: nil},
		fakeFactory(&fakeStorage{}),
	)
	require.NoError(t, err)
	require.Equal(t, "s3", adapter.Driver())
}

// TestLazyImageGenerationStorage_BucketIsPrefix 验证 Bucket() 返回前缀（用于状态展示）。
func TestLazyImageGenerationStorage_BucketIsPrefix(t *testing.T) {
	adapter, err := ProvideImageGenerationStorage(
		&fakeConfigReader{cfg: nil},
		fakeFactory(&fakeStorage{}),
	)
	require.NoError(t, err)
	require.Equal(t, service.ImageGenerationPrefix, adapter.Bucket())
}

// TestProvideEnovaImageAssetStorage_LocalMode 验证 storage_driver=local 时选择 local 驱动。
func TestProvideEnovaImageAssetStorage_LocalMode(t *testing.T) {
	// local 模式不使用 reader/factory，传 nil 即可
	cfg := newTestConfig(t.TempDir(), "local", "")
	storage, err := ProvideEnovaImageAssetStorage(cfg, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "local", storage.Driver())
}

// TestProvideEnovaImageAssetStorage_S3Default 验证 storage_driver 留空时默认 s3 懒加载。
func TestProvideEnovaImageAssetStorage_S3Default(t *testing.T) {
	cfg := newTestConfig(t.TempDir(), "", "")
	// reader 传 nil 模拟未配置
	storage, err := ProvideEnovaImageAssetStorage(cfg, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "s3", storage.Driver())
	require.False(t, storage.Configured(), "未配置时应 Configured()=false")
}

// TestProvideEnovaImageAssetStorage_UnknownDriverReturnsError 验证未知驱动报错。
func TestProvideEnovaImageAssetStorage_UnknownDriverReturnsError(t *testing.T) {
	cfg := newTestConfig(t.TempDir(), "unknown", "")
	_, err := ProvideEnovaImageAssetStorage(cfg, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown storage_driver")
}
