// Package service 提供 Agnes 2.0 Flash 多模态聊天图片适配器。
//
// 适配器负责将下游 OpenAI Chat Completions 请求中的 data:image/...;base64 图片
// 上传到 Cloudflare R2，并替换为公网可访问的 HTTPS URL，再以原始 CC 转发到 Agnes 上游。
//
// 仅对 platform=openai && type=apikey 且 Extra["agnes_chat_image_adapter"]=true 的账号生效。
package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// AgnesChatImageAdapter 负责将下游 OpenAI Chat Completions 请求中的
// data:image/...;base64 图片上传到 Cloudflare R2，并替换为公网可访问的 HTTPS URL。
//
// 设计约束：
//   - 不下载、不验证公网 HTTPS URL（直接转发给 Agnes）
//   - 对 data URL 严格校验 MIME、base64 有效性、单图大小、单请求图片数量和总大小
//   - 对 file://、blob:、http://、内网地址、非 HTTPS、畸形 data URL 返回 OpenAI 风格 invalid_request_error
//   - 对象 key 使用 16 字节随机前缀，避免被遍历
//   - 不在日志/错误中输出完整 base64 内容、API key、完整签名 URL
type AgnesChatImageAdapter struct {
	storage EnovaImageAssetStorage
	cfg     *config.Config
}

// ProvideAgnesChatImageAdapter 是 wire 工厂。
// storage 可以为 nil 或未配置实例（Configured()=false），适配器在运行时会因此返回 503。
func ProvideAgnesChatImageAdapter(storage EnovaImageAssetStorage, cfg *config.Config) *AgnesChatImageAdapter {
	return &AgnesChatImageAdapter{storage: storage, cfg: cfg}
}

// AgnesChatImageAdapterError 是适配器返回的错误类型。
// 携带 HTTP 状态码与 OpenAI 风格 error type，供调用方写入响应。
type AgnesChatImageAdapterError struct {
	StatusCode int
	ErrType    string
	Message    string
}

func (e *AgnesChatImageAdapterError) Error() string {
	return fmt.Sprintf("agnes_chat_image_adapter: %s: %s", e.ErrType, e.Message)
}

// newInvalidRequestError 构造 OpenAI 风格 invalid_request_error（HTTP 400）。
func newInvalidRequestError(msg string) *AgnesChatImageAdapterError {
	return &AgnesChatImageAdapterError{
		StatusCode: http.StatusBadRequest,
		ErrType:    "invalid_request_error",
		Message:    msg,
	}
}

// newBadGatewayError 构造上游/存储错误（HTTP 502，非客户端错误）。
func newBadGatewayError(msg string) *AgnesChatImageAdapterError {
	return &AgnesChatImageAdapterError{
		StatusCode: http.StatusBadGateway,
		ErrType:    "api_error",
		Message:    msg,
	}
}

// newServiceUnavailableError 构造服务不可用错误（HTTP 503）。
func newServiceUnavailableError(msg string) *AgnesChatImageAdapterError {
	return &AgnesChatImageAdapterError{
		StatusCode: http.StatusServiceUnavailable,
		ErrType:    "api_error",
		Message:    msg,
	}
}

// AdaptBody 遍历请求体，将 data:image/...;base64 图片上传到 R2 并替换为公网 HTTPS URL。
//
// 返回值：
//   - adaptedBody: 改写后的 body；无图片或公网 URL 则原样返回
//   - err: 非空表示校验失败或上传失败，携带 *AgnesChatImageAdapterError 供调用方写入响应
//
// 调用方负责在 err != nil 时写入响应（writeChatCompletionsError）。
func (a *AgnesChatImageAdapter) AdaptBody(ctx context.Context, c *gin.Context, body []byte) ([]byte, error) {
	if a == nil {
		return body, nil
	}
	// 快速短路：没有 messages 字段时原样返回，避免遍历开销
	if !gjson.GetBytes(body, "messages").Exists() {
		return body, nil
	}

	cfg := a.cfg.AgnesChat
	// 配置未启用或未配置 R2 + 存储实例未就绪：对真实 data URL 图片块返回 503，其他请求透传
	storageReady := a.storage != nil && a.storage.Configured() && cfg.Active()
	if !storageReady {
		// 必须使用结构化检测，不能全文匹配 "data:image" 关键字，
		// 否则用户在文本中提到 "data:image" 或 "image_url" 会被误判为图片请求。
		if hasDataURLImageBlock(body) {
			return body, newServiceUnavailableError("Agnes chat image adapter is not configured")
		}
		// 无真实 data URL 图片块：原样转发，让上游自然处理（公网 URL 或纯文本）
		return body, nil
	}

	userID, _ := ctx.Value(ctxkey.UserID).(int64)

	maxImages := cfg.MaxImagesPerRequest
	maxImageBytes := cfg.MaxImageBytes
	maxTotalBytes := cfg.MaxTotalBytes

	var (
		imageCount   int
		totalDecoded int64
		mutated      = false
		adaptedBody  = body
	)

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, nil
	}

	for msgIdx, msg := range messages.Array() {
		contentField := msg.Get("content")
		if !contentField.Exists() {
			continue
		}

		// content 为字符串：无图片，跳过
		if contentField.Type == gjson.String {
			continue
		}
		if !contentField.IsArray() {
			continue
		}

		for partIdx, part := range contentField.Array() {
			partType := part.Get("type").String()
			if partType != "image_url" {
				continue
			}

			urlField := part.Get("image_url.url")
			if !urlField.Exists() || urlField.Type != gjson.String {
				continue
			}
			rawURL := urlField.String()
			if rawURL == "" {
				continue
			}

			imageCount++
			if imageCount > maxImages {
				return body, newInvalidRequestError(fmt.Sprintf(
					"too many images in request: max %d allowed, got at least %d",
					maxImages, imageCount,
				))
			}

			newURL, isDataURL, err := a.processImageURL(ctx, rawURL, userID, maxImageBytes, &totalDecoded, maxTotalBytes)
			if err != nil {
				return body, err
			}
			if !isDataURL {
				// 公网 URL 透传，不改写
				continue
			}
			if newURL == rawURL {
				continue
			}

			path := fmt.Sprintf("messages.%d.content.%d.image_url.url", msgIdx, partIdx)
			updated, sErr := sjson.SetBytes(adaptedBody, path, newURL)
			if sErr != nil {
				return body, fmt.Errorf("sjson set image url: %w", sErr)
			}
			adaptedBody = updated
			mutated = true
		}
	}

	if !mutated {
		return body, nil
	}
	return adaptedBody, nil
}

// processImageURL 处理单个 image_url.url。
//
// 返回值：
//   - newURL: 替换后的 URL（data URL 时为 R2 HTTPS URL；其他情况原样返回）
//   - isDataURL: 是否为 data:image/...;base64 URL
//   - err: 校验/上传失败时非空
//
// totalDecoded 累计解码字节数，用于校验单请求总字节上限。
func (a *AgnesChatImageAdapter) processImageURL(
	ctx context.Context,
	rawURL string,
	userID int64,
	maxImageBytes int64,
	totalDecoded *int64,
	maxTotalBytes int64,
) (string, bool, error) {
	// 分支 1：data URL
	if strings.HasPrefix(rawURL, "data:") {
		newURL, err := a.processDataURL(ctx, rawURL, userID, maxImageBytes, totalDecoded, maxTotalBytes)
		if err != nil {
			return rawURL, true, err
		}
		return newURL, true, nil
	}

	// 分支 2：公网 HTTPS URL — 直接透传，不下载、不验证、不改写
	if isPublicHTTPSURL(rawURL) {
		return rawURL, false, nil
	}

	// 分支 3：所有其他 URL（file://, blob:, http://, 内网地址, 畸形 URL）
	return rawURL, false, newInvalidRequestError(
		"unsupported image_url.url: only public HTTPS URLs or data:image/{png,jpeg,webp};base64 URLs are allowed",
	)
}

// isPublicHTTPSURL 校验 URL 为 HTTPS 且主机解析为公网 IP。
// 校验失败（私有/内网/localhost/非 HTTPS/畸形）返回 false。
func isPublicHTTPSURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	// 直接 IP
	if ip := net.ParseIP(host); ip != nil {
		return isPublicNetIP(ip)
	}
	// 主机名：禁止内网域名
	lower := strings.ToLower(host)
	switch {
	case lower == "localhost", strings.HasSuffix(lower, ".localhost"):
		return false
	case strings.HasSuffix(lower, ".internal"), strings.HasSuffix(lower, ".local"):
		return false
	}
	// DNS 解析后校验
	ips, err := net.LookupIP(host)
	if err != nil {
		// DNS 失败按非公网处理（避免误放行）
		return false
	}
	for _, ip := range ips {
		if !isPublicNetIP(ip) {
			return false
		}
	}
	return true
}

// isPublicNetIP 报告 ip 是否为公网地址。
func isPublicNetIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return true
}

// processDataURL 解析并上传 data URL，返回公网 HTTPS URL。
func (a *AgnesChatImageAdapter) processDataURL(
	ctx context.Context,
	rawURL string,
	userID int64,
	maxImageBytes int64,
	totalDecoded *int64,
	maxTotalBytes int64,
) (string, error) {
	mimeType, b64Data, err := parseDataURL(rawURL)
	if err != nil {
		return "", newInvalidRequestError(err.Error())
	}
	if !isAllowedImageMime(mimeType) {
		return "", newInvalidRequestError(fmt.Sprintf(
			"unsupported image mime type: %s; only image/png, image/jpeg, image/webp are allowed",
			mimeType,
		))
	}

	// 粗略预估解码后大小（base64 编码后约为原始大小的 4/3）
	estimated := int64(len(b64Data)) * 3 / 4
	if estimated > maxImageBytes {
		return "", newInvalidRequestError(fmt.Sprintf(
			"image exceeds max size %d bytes (estimated %d)",
			maxImageBytes, estimated,
		))
	}

	// 累计总字节校验
	if *totalDecoded+estimated > maxTotalBytes {
		return "", newInvalidRequestError(fmt.Sprintf(
			"total image bytes exceed max %d (current %d + estimated %d)",
			maxTotalBytes, *totalDecoded, estimated,
		))
	}

	// 校验解码后内容与声明 MIME 一致：读取前 N 字节匹配 magic bytes。
	// 防止把任意二进制（HTML、可执行文件等）伪装成 image/png 上传到公开桶。
	if err := verifyImageMagicBytes(b64Data, mimeType); err != nil {
		return "", newInvalidRequestError(err.Error())
	}

	// 生成对象 key：{user_id}/{random16hex}.{ext}
	ext := mimeExtension(mimeType)
	userPrefix := ""
	if userID > 0 {
		userPrefix = fmt.Sprintf("%d/", userID)
	}
	destKey := userPrefix + randomHexKey(16) + ext

	// 复用现有的 decodeAndStoreBase64Image：流式解码 + 上传 + 超限回滚删除
	stored, err := decodeAndStoreBase64Image(ctx, b64Data, destKey, mimeType, a.storage, maxImageBytes)
	if err != nil {
		// 区分存储未配置 vs 上传失败
		if errors.Is(err, errStorageNotConfigured) {
			return "", newServiceUnavailableError("Agnes chat image storage is not configured")
		}
		// 上传失败：返回 502，不泄露内部错误细节
		logger.L().Warn("agnes_chat_image_adapter: upload failed",
			zap.Int64("user_id", userID),
			zap.String("mime", mimeType),
			zap.Int64("estimated_bytes", estimated),
			zap.Error(err),
		)
		return "", newBadGatewayError("failed to upload image to storage")
	}

	*totalDecoded += stored.Size

	// 生成公网访问 URL
	accessURL, err := a.storage.PresignGet(ctx, stored.Key, 0)
	if err != nil {
		// 上传成功但 presign 失败：清理已上传对象
		_ = a.storage.Delete(ctx, stored.Key)
		logger.L().Warn("agnes_chat_image_adapter: presign get failed",
			zap.Int64("user_id", userID),
			zap.Error(err),
		)
		return "", newBadGatewayError("failed to generate image access URL")
	}

	logger.L().Debug("agnes_chat_image_adapter: uploaded data url",
		zap.Int64("user_id", userID),
		zap.String("mime", mimeType),
		zap.Int64("size", stored.Size),
		zap.String("bucket", stored.Bucket),
	)
	return accessURL, nil
}

// imageMagicBytes 是各图片格式的文件签名（magic bytes）。
// JPEG 的 FFX8FF 有多种形式，统一用前 3 字节 FFX8FF 匹配。
var imageMagicBytes = map[string][][]byte{
	"image/png":  {{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}}, // \x89PNG\r\n\x1A\n
	"image/jpeg": {{0xFF, 0xD8, 0xFF}},                                  // SOI + 任意 JPEG marker
	"image/webp": {{0x52, 0x49, 0x46, 0x46}},                            // "RIFF"（WebP 容器前缀，后续 4 字节长度 + "WEBP"）
}

// verifyImageMagicBytes 解码 base64 前缀并校验文件签名是否匹配声明 MIME。
// 仅解码前 32 字节（足够覆盖所有支持的 magic bytes），避免一次性解码大文件。
func verifyImageMagicBytes(b64Data string, declaredMime string) error {
	signatures, ok := imageMagicBytes[declaredMime]
	if !ok {
		// 未在表中登记的允许 MIME（理论不会到这里，前面已校验 isAllowedImageMime）
		return fmt.Errorf("no magic bytes rule for mime %s", declaredMime)
	}

	// base64.StdEncoding 每 4 字符解码 3 字节；解码前 44 字符可得到前 33 字节，
	// 足够覆盖 PNG(8)/JPEG(3)/WebP(12) 的 magic bytes。
	prefixLen := 44
	if len(b64Data) < prefixLen {
		prefixLen = len(b64Data)
	}
	// base64 输入长度必须是 4 的倍数，否则解码会失败；向下取整
	prefixLen -= prefixLen % 4
	if prefixLen == 0 {
		return errors.New("image data too short to verify magic bytes")
	}
	decoded, err := base64.StdEncoding.DecodeString(b64Data[:prefixLen])
	if err != nil {
		return errors.New("invalid base64 data while verifying magic bytes")
	}

	for _, sig := range signatures {
		if len(decoded) >= len(sig) && bytes.Equal(decoded[:len(sig)], sig) {
			return nil
		}
	}
	return fmt.Errorf("image content does not match declared mime type %s (magic bytes mismatch)", declaredMime)
}

// parseDataURL 解析 data URL，返回 MIME 类型和 base64 数据。
// 仅接受 data:image/...;base64,<data> 格式。
func parseDataURL(raw string) (string, string, error) {
	if !strings.HasPrefix(raw, "data:") {
		return "", "", errors.New("malformed data URL: missing data: prefix")
	}
	rest := raw[len("data:"):]

	commaIdx := strings.Index(rest, ",")
	if commaIdx < 0 {
		return "", "", errors.New("malformed data URL: missing comma")
	}
	meta := rest[:commaIdx]
	data := rest[commaIdx+1:]
	if data == "" {
		return "", "", errors.New("malformed data URL: empty data")
	}

	// meta 格式：<mediatype>;base64 或 <mediatype>
	if !strings.HasSuffix(meta, ";base64") {
		return "", "", errors.New("malformed data URL: only base64 encoding is supported")
	}
	mimeType := strings.TrimSuffix(meta, ";base64")
	if mimeType == "" {
		return "", "", errors.New("malformed data URL: missing mime type")
	}
	// 规范化 MIME（去 charset 等）
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if mimeType == "" {
		return "", "", errors.New("malformed data URL: empty mime type after parse")
	}

	// 校验 base64 有效性
	if !isValidBase64(data) {
		return "", "", errors.New("malformed data URL: invalid base64 data")
	}

	return mimeType, data, nil
}

// isValidBase64 流式校验 base64 字符串有效性。
// 不一次性解码以避免大字符串 OOM；只校验前 64KB 以避免 CPU 开销，
// 实际解码由 decodeAndStoreBase64Image 的 LimitReader 处理。
func isValidBase64(s string) bool {
	if s == "" {
		return false
	}
	// base64.StdEncoding 要求长度为 4 倍数
	if len(s)%4 != 0 {
		return false
	}
	dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(s))
	buf := make([]byte, 4096)
	totalRead := 0
	for {
		n, err := dec.Read(buf)
		totalRead += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				return true
			}
			return false
		}
		if n == 0 {
			return false
		}
		if totalRead >= 65536 {
			// 校验前 64KB 已通过，剩余交给流式解码
			return true
		}
	}
}

// mimeExtension 返回 MIME 类型对应的文件扩展名（含点）。
func mimeExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

// randomHexKey 生成 n 字节的随机十六进制字符串（2n 字符）。
// 用于对象 key 前缀，避免可枚举。
func randomHexKey(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// rand.Read 失败几乎不可能；fallback 使用时间戳
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// hasDataURLImageBlock 结构化检测 body 中是否存在真实的 data:image/...;base64 图片块。
//
// 仅遍历 messages[].content[] 中 type=image_url 且 image_url.url 以 "data:image" 开头的元素，
// 避免对纯文本中出现的 "data:image" 或 "image_url" 关键字误判（如用户问"解释 image_url 与 data:image 的区别"）。
func hasDataURLImageBlock(body []byte) bool {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	for _, msg := range messages.Array() {
		contentField := msg.Get("content")
		if !contentField.Exists() || !contentField.IsArray() {
			continue
		}
		for _, part := range contentField.Array() {
			if part.Get("type").String() != "image_url" {
				continue
			}
			urlField := part.Get("image_url.url")
			if !urlField.Exists() || urlField.Type != gjson.String {
				continue
			}
			if strings.HasPrefix(urlField.String(), "data:image") {
				return true
			}
		}
	}
	return false
}
