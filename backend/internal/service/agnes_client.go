package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Agnes 图片生成支持的 size / ratio 枚举（单一权威源，service 与 handler 共用）。
var (
	AgnesAllowedSizes  = []string{"1K", "2K", "3K", "4K"}
	AgnesAllowedRatios = []string{"1:1", "3:4", "4:3", "16:9", "9:16", "2:3", "3:2", "21:9"}
	AgnesAllowedInputMimeTypes = []string{"image/png", "image/jpeg", "image/webp"}
)

// AgnesGenerateRequest 描述一次 Agnes 生图请求（文生图或图生图）。
type AgnesGenerateRequest struct {
	Model  string
	Prompt string
	Size   string
	Ratio  string
	// InputImageBase64 仅图生图使用，作为 extra_body.image 传入。
	// Agnes 期望的是 base64 编码的图片数据（不带 data: 前缀），
	// 而非 URL（传 URL 会被 Agnes 当作 base64 解析报 illegal base64 data 错误）。
	InputImageBase64 []string
}

// AgnesGenerateResult 是 Agnes 上游返回的图片结果。
type AgnesGenerateResult struct {
	// URL 模式：上游返回的临时图片 URL（需转存 S3）
	URL string
	// B64 模式：上游返回的 Base64 图片（需解码后转存 S3）
	B64JSON string
	// MimeType 推断（基于 response 或 url 后缀），默认 image/png
	MimeType string
}

// AgnesCallOptions 调用选项（超时拆分）。
type AgnesCallOptions struct {
	DialTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
	TotalTimeout       time.Duration
}

// AgnesClient 调用 Agnes 图片生成上游。
// 不持有 API Key，每次调用由调用方传入凭据明文（已解密）。
type AgnesClient interface {
	Generate(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error)
}

// agnesClient 是默认实现。
type agnesClient struct {
	baseURL     string
	requestPath string
	httpClient  *http.Client
}

// NewAgnesClient 构造 Agnes 客户端。baseURL 如 https://apihub.agnes-ai.com
func NewAgnesClient(baseURL, requestPath string) AgnesClient {
	if baseURL == "" {
		baseURL = "https://apihub.agnes-ai.com"
	}
	if requestPath == "" {
		requestPath = "/v1/images/generations"
	}
	// 复用一个低默认超时的 client；实际超时由每次调用的 context 控制。
	return &agnesClient{
		baseURL:     strings.TrimRight(baseURL, "/"),
		requestPath: requestPath,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:          10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}
}

// agnesRequestBody 是发送给 Agnes 的请求体。
// 严格遵循上游协议：
//   - response_format 必须放在 extra_body 内，不能放顶层
//   - 图生图的 image 放在 extra_body.image
//   - 不要发送 tags: ["img2img"]
type agnesRequestBody struct {
	Model     string            `json:"model"`
	Prompt    string            `json:"prompt"`
	Size      string            `json:"size"`
	Ratio     string            `json:"ratio,omitempty"`
	ExtraBody agnesExtraBody    `json:"extra_body"`
}

type agnesExtraBody struct {
	ResponseFormat string   `json:"response_format,omitempty"`
	Image          []string `json:"image,omitempty"`
}

// agnesResponse 解析上游响应。
type agnesResponse struct {
	Data []struct {
		URL      string `json:"url"`
		B64JSON  string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
	Error *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Generate 调用 Agnes 生图。返回结果、HTTP 状态码和错误。
// 错误分类由调用方（scheduler）负责。
func (c *agnesClient) Generate(ctx context.Context, apiKey string, req AgnesGenerateRequest, opts AgnesCallOptions) (*AgnesGenerateResult, int, error) {
	if apiKey == "" {
		return nil, 0, errors.New("agnes api key is empty")
	}

	body := agnesRequestBody{
		Model:  req.Model,
		Prompt: req.Prompt,
		Size:   req.Size,
		Ratio:  req.Ratio,
		ExtraBody: agnesExtraBody{
			ResponseFormat: "url",
		},
	}
	// 图生图：把输入图片 base64 数据放到 extra_body.image；不发送 tags
	// Agnes 期望的是 base64 编码数据（不带 data: 前缀），不是 URL
	if len(req.InputImageBase64) > 0 {
		body.ExtraBody.Image = req.InputImageBase64
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal agnes request: %w", err)
	}

	// 用可取消 context 控制总超时；连接/响应头超时通过自定义 transport 控制
	callCtx, cancel := context.WithTimeout(ctx, opts.TotalTimeout)
	defer cancel()

	// 为了支持每次调用不同的 dial/header 超时，按 opts 构造临时 client
	client := c.clientForOpts(opts)

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.baseURL+c.requestPath, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// Authorization 由调用方提供；日志不得打印此 header
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024)) // 限制响应体 8MB
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var parsed agnesResponse
	if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
		// 非 JSON 响应：返回脱敏错误
		return nil, resp.StatusCode, fmt.Errorf("agnes returned non-json response (status %d)", resp.StatusCode)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := "agnes upstream error"
		if parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		return nil, resp.StatusCode, errors.New(sanitizeUpstreamErrorMessage(msg))
	}

	if len(parsed.Data) == 0 {
		return nil, resp.StatusCode, errors.New("agnes returned empty data")
	}

	result := &AgnesGenerateResult{
		URL:      parsed.Data[0].URL,
		B64JSON:  parsed.Data[0].B64JSON,
		MimeType: "image/png",
	}
	if result.URL == "" && result.B64JSON == "" {
		return nil, resp.StatusCode, errors.New("agnes returned no image url or b64_json")
	}
	if result.URL != "" {
		result.MimeType = mimeTypeFromURL(result.URL)
	}
	return result, resp.StatusCode, nil
}

func (c *agnesClient) clientForOpts(opts AgnesCallOptions) *http.Client {
	if opts.TotalTimeout <= 0 {
		return c.httpClient
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   opts.DialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
		},
		Timeout: opts.TotalTimeout,
	}
}

func mimeTypeFromURL(u string) string {
	lower := strings.ToLower(u)
	switch {
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return "image/png"
	}
}

// ValidateAgnesRequest 校验请求参数，返回业务错误（不切换 Key 重试类）。
func ValidateAgnesRequest(req AgnesGenerateRequest, maxPromptChars int) error {
	if strings.TrimSpace(req.Prompt) == "" {
		return errImagePromptRequired()
	}
	if maxPromptChars > 0 && len(req.Prompt) > maxPromptChars {
		return errImageInvalidRequest(fmt.Sprintf("prompt exceeds %d chars", maxPromptChars))
	}
	if !sliceContains(AgnesAllowedSizes, req.Size) {
		return errImageInvalidSize(req.Size)
	}
	if !sliceContains(AgnesAllowedRatios, req.Ratio) {
		return errImageInvalidRatio(req.Ratio)
	}
	return nil
}

func sliceContains(slice []string, v string) bool {
	for _, s := range slice {
		if s == v {
			return true
		}
	}
	return false
}
