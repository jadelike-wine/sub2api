package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pngBytes is a minimal payload whose signature makes http.DetectContentType
// report image/png.
var pngBytes = []byte("\x89PNG\r\n\x1a\nfake-png-payload")

type savedImage struct {
	key         string
	contentType string
	data        []byte
}

type fakeImageStorage struct {
	saved []savedImage
	url   string
	err   error
}

func (f *fakeImageStorage) Save(_ context.Context, key, contentType string, data []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.saved = append(f.saved, savedImage{key: key, contentType: contentType, data: append([]byte(nil), data...)})
	if f.url != "" {
		return f.url, nil
	}
	return "https://cdn.test/" + key, nil
}

func TestImageResultUploaderRewritesB64JSON(t *testing.T) {
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"created":1,"data":[{"b64_json":"` + b64 + `","revised_prompt":"a cat"}]}`)

	out, err := uploader.Rewrite(context.Background(), "imgtask_abc", result)
	require.NoError(t, err)

	require.Len(t, storage.saved, 1)
	require.Equal(t, "images/imgtask_abc-0.png", storage.saved[0].key)
	require.Equal(t, "image/png", storage.saved[0].contentType)
	require.Equal(t, pngBytes, storage.saved[0].data)

	var parsed struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Len(t, parsed.Data, 1)
	require.JSONEq(t, `"https://cdn.test/images/imgtask_abc-0.png"`, string(parsed.Data[0]["url"]))
	_, hasB64 := parsed.Data[0]["b64_json"]
	require.False(t, hasB64, "b64_json must be stripped after offload")
	require.JSONEq(t, `"a cat"`, string(parsed.Data[0]["revised_prompt"]), "unrelated fields preserved")
}

func TestImageResultUploaderRewritesURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer upstream.Close()

	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)

	result := json.RawMessage(`{"created":1,"data":[{"url":"` + upstream.URL + `/pic.png"}]}`)
	out, err := uploader.Rewrite(context.Background(), "imgtask_xyz", result)
	require.NoError(t, err)

	require.Len(t, storage.saved, 1)
	require.Equal(t, pngBytes, storage.saved[0].data)
	require.Equal(t, "image/png", storage.saved[0].contentType)

	var parsed struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.JSONEq(t, `"https://cdn.test/images/imgtask_xyz-0.png"`, string(parsed.Data[0]["url"]))
}

func TestImageResultUploaderPropagatesStorageError(t *testing.T) {
	storage := &fakeImageStorage{err: errors.New("bucket unreachable")}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"data":[{"b64_json":"` + b64 + `"}]}`)

	_, err := uploader.Rewrite(context.Background(), "imgtask_err", result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bucket unreachable")
}

func TestImageResultUploaderNilStoragePassthrough(t *testing.T) {
	var uploader *ImageResultUploader
	result := json.RawMessage(`{"data":[{"url":"https://example.test/x.png"}]}`)
	out, err := uploader.Rewrite(context.Background(), "imgtask_nil", result)
	require.NoError(t, err)
	require.JSONEq(t, string(result), string(out))
}

func TestImageTaskServiceCompleteOffloadsToStorage(t *testing.T) {
	store := &imageTaskMemoryStore{}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	require.True(t, svc.Enabled())

	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"created":1,"data":[{"b64_json":"` + b64 + `"}]}`)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, result))

	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, got.Status)
	require.Equal(t, "https://cdn.test/images/"+created.ID+"-0.png", got.ImageURL)
	require.NotContains(t, string(got.Result), "b64_json", "large base64 must not be persisted to Redis")
	require.Len(t, storage.saved, 1)
}

func TestImageTaskServiceCompleteOffloadFailureMarksFailed(t *testing.T) {
	store := &imageTaskMemoryStore{}
	storage := &fakeImageStorage{err: errors.New("bucket unreachable")}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)

	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"data":[{"b64_json":"` + b64 + `"}]}`)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, result))

	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	require.Contains(t, string(got.Error), "object storage")
	require.NotContains(t, string(got.Result), "b64_json", "failed offload must not persist base64 to Redis")
}

// ─── H1: 新生图 key 不再使用 media/images/ 前缀 ───

// TestImageAssetService_BuildUserUploadKey_NewFormat 验证 buildUserUploadKey 生成的 key
// 不以 media/images/ 开头（否则会被 fullKey 误判为旧 key 而跳过 image-generation/ 前缀）。
func TestImageAssetService_BuildUserUploadKey_NewFormat(t *testing.T) {
	svc := &ImageAssetService{}
	key := svc.buildUserUploadKey(42, "image/png")

	require.False(t, strings.HasPrefix(key, "media/images/"),
		"新 key 不得以 media/images/ 开头，否则 fullKey 会跳过 image-generation/ 前缀，got: %s", key)
	require.True(t, strings.HasPrefix(key, "42/"),
		"新 key 应以 {user_id}/ 开头，got: %s", key)
	require.Contains(t, key, "/uploads/", "上传 key 应包含 /uploads/ 段")
	require.True(t, strings.HasSuffix(key, ".png"), "png key 应以 .png 结尾")
}

// TestImageAssetService_IsOwnedByUser_AcceptsBothFormats 验证 isOwnedByUser 同时接受
// 新格式（{user_id}/...）和旧格式（media/images/{user_id}/...），并拒绝其他用户目录。
func TestImageAssetService_IsOwnedByUser_AcceptsBothFormats(t *testing.T) {
	svc := &ImageAssetService{}

	// 新格式：归属当前用户
	require.True(t, svc.isOwnedByUser("42/2024/01/uploads/abc.png", 42))
	// 旧格式：归属当前用户（数据库既有记录兼容）
	require.True(t, svc.isOwnedByUser("media/images/42/2024/01/uploads/abc.png", 42))

	// 新格式：归属其他用户
	require.False(t, svc.isOwnedByUser("43/2024/01/uploads/abc.png", 42))
	// 旧格式：归属其他用户
	require.False(t, svc.isOwnedByUser("media/images/43/2024/01/uploads/abc.png", 42))

	// 边界：用户 1 不应匹配用户 10 的目录（前缀 "1/" 不匹配 "10/"）
	require.False(t, svc.isOwnedByUser("10/2024/01/uploads/abc.png", 1))
	require.False(t, svc.isOwnedByUser("media/images/10/2024/01/uploads/abc.png", 1))
	// 反向：用户 10 不应匹配用户 1 的目录
	require.False(t, svc.isOwnedByUser("1/2024/01/uploads/abc.png", 10))

	// 任意 key（无 user_id 段）应拒绝
	require.False(t, svc.isOwnedByUser("random/key.png", 42))
	require.False(t, svc.isOwnedByUser("", 42))
}

// TestImageGenerationService_BuildKeys_NewFormat 验证 buildOutputS3Key 和 BuildInputS3Key
// 生成的 key 不以 media/images/ 开头。
func TestImageGenerationService_BuildKeys_NewFormat(t *testing.T) {
	svc := &ImageGenerationService{}

	outputKey := svc.buildOutputS3Key(42, 1, 2, "image/jpeg")
	require.False(t, strings.HasPrefix(outputKey, "media/images/"),
		"输出 key 不得以 media/images/ 开头，got: %s", outputKey)
	require.True(t, strings.HasPrefix(outputKey, "42/"), "输出 key 应以 {user_id}/ 开头")
	require.Contains(t, outputKey, "/1/2/output/", "输出 key 应包含 conversation/generation/output 段")
	require.True(t, strings.HasSuffix(outputKey, ".jpg"), "jpeg key 应以 .jpg 结尾")

	inputKey := svc.BuildInputS3Key(42, "image/webp")
	require.False(t, strings.HasPrefix(inputKey, "media/images/"),
		"输入 key 不得以 media/images/ 开头，got: %s", inputKey)
	require.True(t, strings.HasPrefix(inputKey, "42/"), "输入 key 应以 {user_id}/ 开头")
	require.Contains(t, inputKey, "/uploads/", "输入 key 应包含 /uploads/ 段")
	require.True(t, strings.HasSuffix(inputKey, ".webp"), "webp key 应以 .webp 结尾")
}
