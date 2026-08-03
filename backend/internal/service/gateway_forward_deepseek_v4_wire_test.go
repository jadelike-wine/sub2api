package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestGatewayForward_DeepSeekV4_AutoThinkingWireBody 验证 Anthropic /v1/messages
// 标准转发路径在模型映射到 deepseek-v4-flash 后，最终发送给上游的 HTTP body 中
// thinking.type 为 adaptive（auto → adaptive），且不含 budget_tokens。
//
// 该测试断言的是 mock upstream 实际接收到的 wire body 字节，而非中间内存对象，
// 用于捕获"重写了内存对象但最终用了原始 body"这类回归。
func TestGatewayForward_DeepSeekV4_AutoThinkingWireBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 入站请求：claude-opus-5 + thinking.type=auto（流式，复刻生产场景）。
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"auto","budget_tokens":10000},"max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	// 上游返回最小合法 SSE 流。
	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_ds_v4","type":"message","role":"assistant","content":[],"model":"deepseek-v4-flash","stop_reason":"","usage":{"input_tokens":5}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid_ds_v4_wire"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := &Account{
		ID:          32,
		Name:        "sensenova-Unrobed0099",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-deepseek-key",
			"base_url": "https://api.deepseek.com",
			"model_mapping": map[string]any{
				"claude-opus-5": "deepseek-v4-flash",
			},
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	// 断言 1：最终模型为 deepseek-v4-flash（模型映射生效）。
	require.Equal(t, "deepseek-v4-flash", gjson.GetBytes(upstream.lastBody, "model").String(),
		"最终上游 body 的 model 应为映射后的 deepseek-v4-flash")

	// 断言 2：thinking.type 为 adaptive（auto → adaptive 转换生效）。
	require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String(),
		"auto 必须被转换为 adaptive，否则上游会返回 400")

	// 断言 3：budget_tokens 已被移除（adaptive 模式不允许 budget_tokens）。
	require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists(),
		"auto→adaptive 后不应携带 budget_tokens")
}

// TestGatewayForward_DeepSeekV4_AdaptiveStaysAdaptiveWireBody 验证 thinking.type=adaptive 输入时
// 最终 wire body 仍为 adaptive（保持不变，不应被错误转换为 auto）。
func TestGatewayForward_DeepSeekV4_AdaptiveStaysAdaptiveWireBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive","budget_tokens":10000},"max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_ds_v4_adaptive","type":"message","role":"assistant","content":[],"model":"deepseek-v4-flash","stop_reason":"","usage":{"input_tokens":5}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid_ds_v4_adaptive_wire"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := &Account{
		ID:          32,
		Name:        "sensenova-Unrobed0099",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-deepseek-key",
			"base_url": "https://api.deepseek.com",
			"model_mapping": map[string]any{
				"claude-opus-5": "deepseek-v4-flash",
			},
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "deepseek-v4-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String(),
		"adaptive 输入应保持 adaptive，不得被转换为 auto")
	require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists(),
		"adaptive 模式不应携带 budget_tokens")
}

// TestGatewayForward_DeepSeekV4_EnabledPreservesBudgetTokens 验证 thinking.type=enabled
// 时 budget_tokens 被保留（enabled 是唯一允许 budget_tokens 的模式）。
func TestGatewayForward_DeepSeekV4_EnabledPreservesBudgetTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":8192},"max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_ds_v4_enabled","type":"message","role":"assistant","content":[],"model":"deepseek-v4-flash","stop_reason":"","usage":{"input_tokens":5}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid_ds_v4_enabled_wire"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := &Account{
		ID:          32,
		Name:        "sensenova-Unrobed0099",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-deepseek-key",
			"base_url": "https://api.deepseek.com",
			"model_mapping": map[string]any{
				"claude-opus-5": "deepseek-v4-flash",
			},
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "deepseek-v4-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "enabled", gjson.GetBytes(upstream.lastBody, "thinking.type").String(),
		"enabled 应保持 enabled")
	require.Equal(t, int64(8192), gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Int(),
		"enabled 模式必须保留 budget_tokens")
}

// TestGatewayForward_DeepSeekV4_NoThinkingFieldStaysAbsent 验证未传 thinking 时
// 最终 wire body 不生成 thinking 字段。
func TestGatewayForward_DeepSeekV4_NoThinkingFieldStaysAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-opus-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_ds_v4_none","type":"message","role":"assistant","content":[],"model":"deepseek-v4-flash","stop_reason":"","usage":{"input_tokens":5}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid_ds_v4_none_wire"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := &Account{
		ID:          32,
		Name:        "sensenova-Unrobed0099",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-deepseek-key",
			"base_url": "https://api.deepseek.com",
			"model_mapping": map[string]any{
				"claude-opus-5": "deepseek-v4-flash",
			},
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "deepseek-v4-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "thinking").Exists(),
		"未传 thinking 时不应自动生成字段")
}

// TestGatewayForward_NonDeepSeekV4_DoesNotConvertThinking 验证非 DeepSeek V4 模型
// 不触发 thinking 适配（回归测试，确保转换仅对 deepseek-v4 前缀生效）。
func TestGatewayForward_NonDeepSeekV4_DoesNotConvertThinking(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 使用 deepseek-reasoner（非 v4），thinking.type=adaptive 应原样保留。
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive"},"max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_non_ds_v4","type":"message","role":"assistant","content":[],"model":"deepseek-reasoner","stop_reason":"","usage":{"input_tokens":5}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid_non_ds_v4_wire"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := &Account{
		ID:          33,
		Name:        "non-deepseek-v4-acct",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-key",
			"base_url": "https://api.deepseek.com",
			"model_mapping": map[string]any{
				"claude-opus-5": "deepseek-reasoner",
			},
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "deepseek-reasoner", gjson.GetBytes(upstream.lastBody, "model").String())
	// 非 DeepSeek V4 模型的 thinking.type 应原样保留（不做转换）。
	require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String(),
		"非 DeepSeek V4 模型不应触发 thinking 适配")
}

// 编译期断言：确保 bytes 包被使用（用于未来扩展 wire body 字节级断言）。
var _ = bytes.Equal

// ============================================================================
// SenseNova 上游 wire body 测试
// 验证 /v1/messages 标准转发路径在 SenseNova 上游下正确转换 thinking.type。
// SenseNova (token.sensenova.cn) 仅接受 enabled | disabled | auto，不接受 adaptive。
// ============================================================================

// sensenovaWireTestAccount 创建一个指向 SenseNova 上游的测试账号。
func sensenovaWireTestAccount() *Account {
	return &Account{
		ID:          42,
		Name:        "sensenova-test",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sensenova-key",
			"base_url": "https://token.sensenova.cn",
			"model_mapping": map[string]any{
				"claude-opus-5": "deepseek-v4-flash",
			},
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func sensenovaWireTestConfig() *config.Config {
	return &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowInsecureHTTP: true,
			},
		},
	}
}

func sensenovaWireTestUpstream() *anthropicHTTPUpstreamRecorder {
	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_sensenova","type":"message","role":"assistant","content":[],"model":"deepseek-v4-flash","stop_reason":"","usage":{"input_tokens":5}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	return &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid_sensenova_wire"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}
}

// TestGatewayForward_SenseNova_AdaptiveToAutoWireBody 验证 SenseNova 上游将
// thinking.type=adaptive 转换为 auto（SenseNova 不接受 adaptive）。
func TestGatewayForward_SenseNova_AdaptiveToAutoWireBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive","budget_tokens":10000},"max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := sensenovaWireTestUpstream()
	cfg := sensenovaWireTestConfig()
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := sensenovaWireTestAccount()
	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "deepseek-v4-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "auto", gjson.GetBytes(upstream.lastBody, "thinking.type").String(),
		"SenseNova: adaptive 必须被转换为 auto，否则上游返回 400")
	require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists(),
		"SenseNova: adaptive→auto 后不应携带 budget_tokens")
}

// TestGatewayForward_SenseNova_AutoStaysAutoWireBody 验证 SenseNova 上游
// thinking.type=auto 保持不变，但 budget_tokens 被移除。
func TestGatewayForward_SenseNova_AutoStaysAutoWireBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"auto","budget_tokens":10000},"max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := sensenovaWireTestUpstream()
	cfg := sensenovaWireTestConfig()
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := sensenovaWireTestAccount()
	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "auto", gjson.GetBytes(upstream.lastBody, "thinking.type").String(),
		"SenseNova: auto 应保持不变")
	require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists(),
		"SenseNova: auto 模式不应携带 budget_tokens")
}

// TestGatewayForward_SenseNova_EnabledPreservesBudgetTokens 验证 SenseNova 上游
// thinking.type=enabled 保持不变，且 budget_tokens 被保留。
func TestGatewayForward_SenseNova_EnabledPreservesBudgetTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":10000},"max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := sensenovaWireTestUpstream()
	cfg := sensenovaWireTestConfig()
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := sensenovaWireTestAccount()
	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "enabled", gjson.GetBytes(upstream.lastBody, "thinking.type").String(),
		"SenseNova: enabled 应保持不变")
	require.Equal(t, int64(10000), gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Int(),
		"SenseNova: enabled 模式应保留 budget_tokens")
}

// TestGatewayForward_SenseNova_DisabledStripsBudgetTokens 验证 SenseNova 上游
// thinking.type=disabled 保持不变，但 budget_tokens 被移除。
func TestGatewayForward_SenseNova_DisabledStripsBudgetTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"disabled","budget_tokens":10000},"max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-opus-5",
		Stream: true,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := sensenovaWireTestUpstream()
	cfg := sensenovaWireTestConfig()
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := sensenovaWireTestAccount()
	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "disabled", gjson.GetBytes(upstream.lastBody, "thinking.type").String(),
		"SenseNova: disabled 应保持不变")
	require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists(),
		"SenseNova: disabled 模式不应携带 budget_tokens")
}
