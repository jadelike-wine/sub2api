//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestS3Storage_FullKey_LegacyMediaImagesKeySkipsPrefix 验证旧 key（media/images/...）
// 不会叠加新前缀。这是 H1 修复的核心：升级前对象位于 media/images/{user_id}/...，
// 升级后 lazy storage 固定 Prefix=image-generation，若叠加会指向不存在的对象。
func TestS3Storage_FullKey_LegacyMediaImagesKeySkipsPrefix(t *testing.T) {
	s := &S3EnovaImageAssetStorage{
		bucket: "b",
		prefix: service.ImageGenerationPrefix,
	}

	cases := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "legacy upload key",
			key:  "media/images/42/2024/01/uploads/abc.png",
			want: "media/images/42/2024/01/uploads/abc.png",
		},
		{
			name: "legacy output key",
			key:  "media/images/42/2024/01/1/2/output/xyz.png",
			want: "media/images/42/2024/01/1/2/output/xyz.png",
		},
		{
			name: "legacy key with leading slash",
			key:  "/media/images/42/2024/01/uploads/abc.png",
			want: "media/images/42/2024/01/uploads/abc.png",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, s.fullKey(tc.key),
				"旧 media/images/ key 应视为绝对路径，不叠加前缀")
		})
	}
}

// TestS3Storage_FullKey_NewKeyAppliesPrefix 验证非旧 key 仍正常叠加前缀。
func TestS3Storage_FullKey_NewKeyAppliesPrefix(t *testing.T) {
	s := &S3EnovaImageAssetStorage{
		bucket: "b",
		prefix: service.AgnesChatPrefix,
	}

	cases := []struct {
		name string
		key  string
		want string
	}{
		{name: "agnes chat key", key: "42/abc123.png", want: "agnes-chat/42/abc123.png"},
		{name: "key with leading slash", key: "/42/abc.png", want: "agnes-chat/42/abc.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, s.fullKey(tc.key))
		})
	}
}

// TestS3Storage_FullKey_NewImageGenerationKeyAppliesPrefix 验证新生图 key（{user_id}/...）
// 会叠加 image-generation/ 前缀，确保新对象落入隔离命名空间而非 bucket 根目录。
// 这是 H1 修复的核心断言：buildUserUploadKey/buildOutputS3Key/BuildInputS3Key 生成的
// key 不再以 media/images/ 开头，因此不会被误判为旧 key。
func TestS3Storage_FullKey_NewImageGenerationKeyAppliesPrefix(t *testing.T) {
	s := &S3EnovaImageAssetStorage{
		bucket: "b",
		prefix: service.ImageGenerationPrefix,
	}

	cases := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "new upload key",
			key:  "42/2024/01/uploads/a1b2c3d4.png",
			want: "image-generation/42/2024/01/uploads/a1b2c3d4.png",
		},
		{
			name: "new output key",
			key:  "42/2024/01/1/2/output/xyz.png",
			want: "image-generation/42/2024/01/1/2/output/xyz.png",
		},
		{
			name: "new key with leading slash",
			key:  "/42/2024/01/uploads/abc.png",
			want: "image-generation/42/2024/01/uploads/abc.png",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, s.fullKey(tc.key),
				"新 key 必须叠加 image-generation/ 前缀，不得落入 bucket 根目录")
		})
	}
}

// TestS3Storage_FullKey_EmptyPrefixReturnsKeyAsIs 验证无前缀时原样返回。
func TestS3Storage_FullKey_EmptyPrefixReturnsKeyAsIs(t *testing.T) {
	s := &S3EnovaImageAssetStorage{bucket: "b", prefix: ""}
	require.Equal(t, "media/images/42/uploads/a.png", s.fullKey("media/images/42/uploads/a.png"))
	require.Equal(t, "42/abc.png", s.fullKey("42/abc.png"))
}
