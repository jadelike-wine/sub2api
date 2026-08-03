//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newThinkingDebugObservedLogger 创建一个可观测的 zap logger 及其日志观察器，
// 级别设为 Info 以捕获 logThinkingDebug 输出的 Info 级日志。
func newThinkingDebugObservedLogger(t *testing.T) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.InfoLevel)
	return zap.New(core), logs
}

// thinkingDebugLoggedFields 将一条 observed log 的所有 zap.Field 提取为 map。
// 支持字符串、整数和布尔值三种字段类型。
func thinkingDebugLoggedFields(t *testing.T, logs *observer.ObservedLogs) map[string]any {
	t.Helper()
	entries := logs.All()
	require.Len(t, entries, 1, "expected exactly one log entry")
	fields := map[string]any{}
	for _, f := range entries[0].Context {
		switch f.Type {
		case zapcore.StringType:
			fields[f.Key] = f.String
		case zapcore.Int64Type, zapcore.Int32Type, zapcore.Uint64Type:
			fields[f.Key] = f.Integer
		case zapcore.BoolType:
			fields[f.Key] = f.Integer == 1
		default:
			// 其他类型按字符串兜底
			fields[f.Key] = f.String
		}
	}
	fields["message"] = entries[0].Message
	return fields
}

func TestExtractThinkingInfoFromBody(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		wantType         string
		wantHasBudget    bool
		wantBudgetTokens int64
	}{
		{
			name:             "enabled with budget",
			body:             `{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":32000}}`,
			wantType:         "enabled",
			wantHasBudget:    true,
			wantBudgetTokens: 32000,
		},
		{
			name:             "disabled without budget",
			body:             `{"model":"claude-opus-5","thinking":{"type":"disabled"}}`,
			wantType:         "disabled",
			wantHasBudget:    false,
			wantBudgetTokens: 0,
		},
		{
			name:             "auto without budget",
			body:             `{"model":"claude-opus-5","thinking":{"type":"auto"}}`,
			wantType:         "auto",
			wantHasBudget:    false,
			wantBudgetTokens: 0,
		},
		{
			name:             "no thinking field",
			body:             `{"model":"claude-opus-5","messages":[]}`,
			wantType:         "",
			wantHasBudget:    false,
			wantBudgetTokens: 0,
		},
		{
			name:             "empty body",
			body:             "",
			wantType:         "",
			wantHasBudget:    false,
			wantBudgetTokens: 0,
		},
		{
			name:             "adaptive (non-standard)",
			body:             `{"thinking":{"type":"adaptive"}}`,
			wantType:         "adaptive",
			wantHasBudget:    false,
			wantBudgetTokens: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotHasBudget, gotBudget := extractThinkingInfoFromBody([]byte(tt.body))
			assert.Equal(t, tt.wantType, gotType)
			assert.Equal(t, tt.wantHasBudget, gotHasBudget)
			assert.Equal(t, tt.wantBudgetTokens, gotBudget)
		})
	}
}

// TestExtractThinkingDiagnosticsSafety 验证诊断 helper 对空值、null、非对象、
// 缺少 type 等异常输入不 panic，且安全返回零值。
func TestExtractThinkingDiagnosticsSafety(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantPresent bool
		wantType    string
		wantBudget  bool
	}{
		{name: "nil body", body: "", wantPresent: false, wantType: "", wantBudget: false},
		{name: "no thinking field", body: `{"model":"x","messages":[]}`, wantPresent: false, wantType: "", wantBudget: false},
		{name: "thinking is null", body: `{"thinking":null}`, wantPresent: true, wantType: "", wantBudget: false},
		{name: "thinking is bool", body: `{"thinking":true}`, wantPresent: true, wantType: "", wantBudget: false},
		{name: "thinking is number", body: `{"thinking":42}`, wantPresent: true, wantType: "", wantBudget: false},
		{name: "thinking is string", body: `{"thinking":"enabled"}`, wantPresent: true, wantType: "", wantBudget: false},
		{name: "thinking is array", body: `{"thinking":[1,2]}`, wantPresent: true, wantType: "", wantBudget: false},
		{name: "thinking object missing type", body: `{"thinking":{"budget_tokens":1000}}`, wantPresent: true, wantType: "", wantBudget: true},
		{name: "thinking type is number not string", body: `{"thinking":{"type":123}}`, wantPresent: true, wantType: "", wantBudget: false},
		{name: "thinking type with whitespace", body: `{"thinking":{"type":"  auto  "}}`, wantPresent: true, wantType: "auto", wantBudget: false},
		{name: "invalid json", body: `{not json`, wantPresent: false, wantType: "", wantBudget: false},
		{name: "auto type", body: `{"thinking":{"type":"auto"}}`, wantPresent: true, wantType: "auto", wantBudget: false},
		{name: "adaptive type", body: `{"thinking":{"type":"adaptive"}}`, wantPresent: true, wantType: "adaptive", wantBudget: false},
		{name: "enabled with budget", body: `{"thinking":{"type":"enabled","budget_tokens":8000}}`, wantPresent: true, wantType: "enabled", wantBudget: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				d := extractThinkingDiagnostics([]byte(tt.body))
				assert.Equal(t, tt.wantPresent, d.Present)
				assert.Equal(t, tt.wantType, d.Type)
				assert.Equal(t, tt.wantBudget, d.BudgetTokensPresent)
			})
		})
	}
}

func TestExtractUpstreamHost(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		wantHost string
	}{
		{
			name:     "anthropic default with query",
			rawURL:   "https://api.anthropic.com/v1/messages?beta=true",
			wantHost: "api.anthropic.com",
		},
		{
			name:     "custom base url with proxy query",
			rawURL:   "https://relay.example.com/v1/messages?beta=true&proxy=http%3A%2F%2F10.0.0.1%3A8080",
			wantHost: "relay.example.com",
		},
		{
			name:     "bedrock url",
			rawURL:   "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-opus-5/invoke",
			wantHost: "bedrock-runtime.us-east-1.amazonaws.com",
		},
		{
			name:     "empty url",
			rawURL:   "",
			wantHost: "",
		},
		{
			name:     "invalid url",
			rawURL:   "://not-a-url",
			wantHost: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractUpstreamHost(tt.rawURL)
			assert.Equal(t, tt.wantHost, got)
		})
	}
}

// TestExtractUpstreamEndpointPath 验证仅提取 path，不记录 query 参数。
func TestExtractUpstreamEndpointPath(t *testing.T) {
	assert.Equal(t, "/v1/messages", extractUpstreamEndpointPath("https://api.anthropic.com/v1/messages?beta=true&secret=token"))
	assert.Equal(t, "/model/x/invoke", extractUpstreamEndpointPath("https://bedrock-runtime.us-east-1.amazonaws.com/model/x/invoke"))
	assert.Equal(t, "", extractUpstreamEndpointPath(""))
	assert.Equal(t, "", extractUpstreamEndpointPath("://not-a-url"))
}

func TestInferThinkingTransformRule(t *testing.T) {
	tests := []struct {
		name        string
		incoming    string
		outgoing    string
		mappedModel string
		wantRule    string
		wantApplied bool
	}{
		{
			name:        "no change",
			incoming:    "enabled",
			outgoing:    "enabled",
			mappedModel: "claude-opus-5",
			wantRule:    "",
			wantApplied: false,
		},
		{
			name:        "minimax enabled to adaptive",
			incoming:    "enabled",
			outgoing:    "adaptive",
			mappedModel: "MiniMax-M2",
			wantRule:    "NormalizeChineseLLMThinking",
			wantApplied: true,
		},
		{
			name:        "bedrock enabled to auto",
			incoming:    "enabled",
			outgoing:    "auto",
			mappedModel: "anthropic.claude-opus-5",
			wantRule:    "sanitizeBedrockThinking",
			wantApplied: true,
		},
		{
			name:        "rectify to enabled",
			incoming:    "disabled",
			outgoing:    "enabled",
			mappedModel: "claude-sonnet-4-6",
			wantRule:    "RectifyThinkingBudget",
			wantApplied: true,
		},
		{
			name:        "unknown change",
			incoming:    "enabled",
			outgoing:    "disabled",
			mappedModel: "unknown-model",
			wantRule:    "type_changed:enabled->disabled",
			wantApplied: true,
		},
		// DeepSeek V4: auto → adaptive（原生 DeepSeek 上游的核心转换）
		{
			name:        "deepseek v4 auto to adaptive",
			incoming:    "auto",
			outgoing:    "adaptive",
			mappedModel: "deepseek-v4-flash",
			wantRule:    "NormalizeDeepSeekV4Thinking",
			wantApplied: true,
		},
		// SenseNova: adaptive → auto（SenseNova 上游的核心转换）
		{
			name:        "sensenova adaptive to auto",
			incoming:    "adaptive",
			outgoing:    "auto",
			mappedModel: "deepseek-v4-flash",
			wantRule:    "NormalizeSenseNovaThinking",
			wantApplied: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRule := inferThinkingTransformRule(tt.incoming, tt.outgoing, tt.mappedModel)
			assert.Equal(t, tt.wantRule, gotRule)
			gotApplied := tt.incoming != tt.outgoing
			assert.Equal(t, tt.wantApplied, gotApplied)
		})
	}
}

// TestResolveForwardPath 验证不同账号属性推断出正确的 forward_path 枚举值。
func TestResolveForwardPath(t *testing.T) {
	tests := []struct {
		name     string
		account  *Account
		wantPath string
	}{
		{name: "nil account", account: nil, wantPath: forwardPathUnknown},
		{
			name:     "anthropic passthrough enabled",
			account:  &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"anthropic_passthrough": true}},
			wantPath: forwardPathAnthropicPassthrough,
		},
		{
			name:     "bedrock account",
			account:  &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock},
			wantPath: forwardPathBedrock,
		},
		{
			name:     "vertex service account",
			account:  &Account{Platform: PlatformAnthropic, Type: AccountTypeServiceAccount},
			wantPath: forwardPathVertex,
		},
		{
			name:     "anthropic standard oauth",
			account:  &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			wantPath: forwardPathAnthropicStandard,
		},
		{
			name:     "anthropic standard api key",
			account:  &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			wantPath: forwardPathAnthropicStandard,
		},
		{
			name:     "non-anthropic platform",
			account:  &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			wantPath: forwardPathUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantPath, resolveForwardPath(tt.account))
		})
	}
}

// TestLogThinkingInbound_AutoType 验证客户端发送 {"thinking":{"type":"auto"}}
// 时日志中记录 thinking_type_inbound_raw=auto。
func TestLogThinkingInbound_AutoType(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)
	ctx = context.WithValue(ctx, ctxkey.RequestID, "req-auto")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-auto")

	body := `{"model":"claude-opus-5","thinking":{"type":"auto"},"stream":true}`
	inboundDiag := extractThinkingDiagnostics([]byte(body))
	require.Equal(t, "auto", inboundDiag.Type)

	svc := &GatewayService{}
	svc.logThinkingInbound(ctx, &Account{ID: 1, Name: "test", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, inboundDiag, "claude-opus-5", true)

	fields := thinkingDebugLoggedFields(t, logs)
	assert.Equal(t, "gateway.thinking_debug", fields["message"])
	assert.Equal(t, "gateway.thinking.inbound", fields["event"])
	assert.Equal(t, "auto", fields["thinking_type_inbound_raw"])
	assert.Equal(t, "claude-opus-5", fields["requested_model"])
	assert.Equal(t, "claude-opus-5", fields["mapped_model"])
	assert.Equal(t, true, fields["thinking_present"])
	assert.Equal(t, true, fields["stream"])
	assert.Equal(t, "anthropic_standard", fields["forward_path"])
}

// TestLogThinkingEntryNormalized 验证入口标准化后记录正确的 thinking.type。
// 客户端发送 adaptive 时，入口标准化保留 adaptive（不拒绝），budget_tokens 被移除（若存在）。
func TestLogThinkingEntryNormalized(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)

	// 模拟 NormalizeAnthropicThinking 后的 body：adaptive 保留，无 budget_tokens
	entryBody := `{"model":"claude-opus-5","thinking":{"type":"adaptive"}}`
	entryDiag := extractThinkingDiagnostics([]byte(entryBody))

	svc := &GatewayService{}
	svc.logThinkingEntryNormalized(ctx, nil, "adaptive", entryDiag, "claude-opus-5", false)

	fields := thinkingDebugLoggedFields(t, logs)
	assert.Equal(t, "gateway.thinking.entry_normalized", fields["event"])
	assert.Equal(t, "adaptive", fields["thinking_type_inbound_raw"])
	assert.Equal(t, "adaptive", fields["thinking_type_after_entry_normalize"])
	assert.Equal(t, false, fields["thinking_changed"])
}

// TestLogThinkingRouteResolved_ModelMapping 验证模型映射 claude-opus-5 → deepseek-v4-flash
// 时日志同时保留 requested_model 和 mapped_model。
func TestLogThinkingRouteResolved_ModelMapping(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)
	ctx = context.WithValue(ctx, ctxkey.RequestID, "req-map")

	svc := &GatewayService{}
	svc.logThinkingRouteResolved(ctx,
		&Account{ID: 99, Name: "sensenova-Unrobed0099", Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		"claude-opus-5", "deepseek-v4-flash", "auto", false)

	fields := thinkingDebugLoggedFields(t, logs)
	assert.Equal(t, "gateway.thinking.route_resolved", fields["event"])
	assert.Equal(t, "claude-opus-5", fields["requested_model"])
	assert.Equal(t, "deepseek-v4-flash", fields["mapped_model"])
	assert.Equal(t, "deepseek-v4-flash", fields["mapped_model"])
	assert.Equal(t, "passback_required", fields["thinking_protocol"])
	assert.Equal(t, true, fields["is_deepseek_v4_model"])
	assert.Equal(t, "anthropic_standard", fields["forward_path"])
	assert.Equal(t, int64(99), fields["account_id"])
	assert.Equal(t, "sensenova-Unrobed0099", fields["account_name"])
}

// TestLogThinkingProviderNormalized_DeepSeekV4 验证 DeepSeek V4 适配器执行前后
// 分别记录入口值和 provider 适配后的值（auto → adaptive）。
func TestLogThinkingProviderNormalized_DeepSeekV4(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)

	svc := &GatewayService{}
	svc.logThinkingProviderNormalized(ctx, nil, "claude-opus-5", "deepseek-v4-flash",
		"auto", "adaptive", normalizerDeepSeekV4, false)

	fields := thinkingDebugLoggedFields(t, logs)
	assert.Equal(t, "gateway.thinking.provider_normalized", fields["event"])
	assert.Equal(t, "auto", fields["thinking_type_before_provider_normalize"])
	assert.Equal(t, "adaptive", fields["thinking_type_after_provider_normalize"])
	assert.Equal(t, true, fields["thinking_changed"])
	assert.Equal(t, "deepseek_v4", fields["normalizer"])
}

// TestLogThinkingUpstreamReady_ReadsFromFinalBody 验证上游发送前日志从最终 body
// 重新读取 thinking.type，而不是复用之前的变量。
func TestLogThinkingUpstreamReady_ReadsFromFinalBody(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)
	ctx = context.WithValue(ctx, ctxkey.RequestID, "req-final")

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	// 模拟入口处缓存的原始值
	rememberIncomingThinkingState(c, "auto", "claude-opus-5")
	rememberEntryThinkingType(c, "auto")
	rememberLastUpstreamThinkingType(c, "adaptive")

	// 最终发送的 body（DeepSeek V4 适配后 auto → adaptive）
	finalBody := []byte(`{"model":"deepseek-v4-flash","thinking":{"type":"adaptive"},"messages":[]}`)

	svc := &GatewayService{}
	svc.logThinkingUpstreamReady(ctx, c,
		&Account{ID: 1, Name: "test", Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		finalBody, "https://api.deepseek.com/anthropic/v1/messages?beta=true",
		"claude-opus-5", "deepseek-v4-flash", false)

	fields := thinkingDebugLoggedFields(t, logs)
	assert.Equal(t, "gateway.thinking.upstream_ready", fields["event"])
	// thinking_type_before_upstream 必须从最终 body 重新读取
	assert.Equal(t, "adaptive", fields["thinking_type_before_upstream"])
	// 入口原始值仍保留
	assert.Equal(t, "auto", fields["thinking_type_inbound_raw"])
	assert.Equal(t, "auto", fields["thinking_type_after_entry_normalize"])
	assert.Equal(t, "claude-opus-5", fields["requested_model"])
	assert.Equal(t, "deepseek-v4-flash", fields["mapped_model"])
	assert.Equal(t, true, fields["thinking_transform_applied"])
	assert.Equal(t, "NormalizeDeepSeekV4Thinking", fields["thinking_transform_rule"])
	// upstream_endpoint_path 仅记录 path，不含 query
	assert.Equal(t, "/anthropic/v1/messages", fields["upstream_endpoint_path"])
	assert.Equal(t, "api.deepseek.com", fields["upstream_host"])
}

// TestLogThinkingUpstreamReady_PassthroughOnOff 验证 passthrough 开启和关闭时
// 分别记录正确的 anthropic_passthrough_enabled 和 forward_path。
func TestLogThinkingUpstreamReady_PassthroughOnOff(t *testing.T) {
	cases := []struct {
		name            string
		account         *Account
		wantPassthrough bool
		wantForwardPath string
	}{
		{
			name:            "passthrough enabled",
			account:         &Account{ID: 1, Name: "passthrough-acct", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"anthropic_passthrough": true}},
			wantPassthrough: true,
			wantForwardPath: forwardPathAnthropicPassthrough,
		},
		{
			name:            "passthrough disabled (standard)",
			account:         &Account{ID: 2, Name: "standard-acct", Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			wantPassthrough: false,
			wantForwardPath: forwardPathAnthropicStandard,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observedLog, logs := newThinkingDebugObservedLogger(t)
			ctx := logger.IntoContext(context.Background(), observedLog)
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(nil)

			finalBody := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":1000}}`)
			svc := &GatewayService{}
			svc.logThinkingUpstreamReady(ctx, c, tc.account, finalBody, "https://api.anthropic.com/v1/messages", "claude-opus-5", "claude-opus-5", false)

			fields := thinkingDebugLoggedFields(t, logs)
			assert.Equal(t, tc.wantPassthrough, fields["anthropic_passthrough_enabled"])
			assert.Equal(t, tc.wantForwardPath, fields["forward_path"])
		})
	}
}

// TestLogThinkingUpstreamError_NoSensitiveData 验证上游错误日志中不包含
// 完整 messages、API Key、Authorization、system prompt 等敏感内容。
func TestLogThinkingUpstreamError_NoSensitiveData(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	rememberIncomingThinkingState(c, "adaptive", "claude-opus-5")
	rememberLastUpstreamThinkingType(c, "auto")

	svc := &GatewayService{}
	svc.emitThinkingUpstreamErrorOnFailure(ctx, c,
		&Account{ID: 1, Name: "test", Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		"claude-opus-5", "deepseek-v4-flash")

	// 至少有一条错误日志
	entries := logs.All()
	require.NotEmpty(t, entries, "expected at least one error log")

	for _, entry := range entries {
		for _, f := range entry.Context {
			if f.Type == zapcore.StringType {
				assert.NotContains(t, f.String, "sk-secret", "field %s must not contain API key", f.Key)
				assert.NotContains(t, f.String, "Bearer", "field %s must not contain auth header", f.Key)
				assert.NotContains(t, f.String, "secret user text", "field %s must not contain user text", f.Key)
				assert.NotContains(t, f.String, "system prompt", "field %s must not contain system prompt", f.Key)
			}
		}
	}
}

// TestLogThinkingDebug_NoSensitiveData 验证诊断日志结构体不携带敏感内容。
func TestLogThinkingDebug_NoSensitiveData(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)

	// body 仅用于构造测试场景，不传入日志
	body := `{"model":"claude-opus-5","authorization":"Bearer sk-secret","messages":[{"role":"user","content":"secret user text"}],"thinking":{"type":"enabled","budget_tokens":1000}}`
	inboundDiag := extractThinkingDiagnostics([]byte(body))

	svc := &GatewayService{}
	svc.logThinkingInbound(ctx, nil, inboundDiag, "claude-opus-5", false)

	fields := thinkingDebugLoggedFields(t, logs)
	for key, val := range fields {
		if s, ok := val.(string); ok {
			assert.NotContains(t, s, "sk-secret", "field %s must not contain API key", key)
			assert.NotContains(t, s, "secret user text", "field %s must not contain user text", key)
			assert.NotContains(t, s, "Bearer", "field %s must not contain auth header", key)
		}
	}
	// budget_tokens 只记录数值
	assert.Equal(t, int64(1000), fields["budget_tokens"])
	assert.Equal(t, true, fields["thinking_budget_tokens_present"])
}

func TestRememberAndRecallIncomingThinkingState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	// 空 context 返回空字符串
	assert.Equal(t, "", recallIncomingThinkingType(c))
	assert.Equal(t, "", recallIncomingPublicModel(c))

	// 记忆后可恢复
	rememberIncomingThinkingState(c, "enabled", "claude-opus-5")
	assert.Equal(t, "enabled", recallIncomingThinkingType(c))
	assert.Equal(t, "claude-opus-5", recallIncomingPublicModel(c))

	// nil context 不 panic
	rememberIncomingThinkingState(nil, "enabled", "model")
	assert.Equal(t, "", recallIncomingThinkingType(nil))
	assert.Equal(t, "", recallIncomingPublicModel(nil))
}

// TestRememberAndRecallEntryAndUpstreamTypes 验证 entry / lastUpstream 缓存。
func TestRememberAndRecallEntryAndUpstreamTypes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	assert.Equal(t, "", recallEntryThinkingType(c))
	assert.Equal(t, "", recallLastUpstreamThinkingType(c))

	rememberEntryThinkingType(c, "adaptive")
	rememberLastUpstreamThinkingType(c, "auto")
	assert.Equal(t, "adaptive", recallEntryThinkingType(c))
	assert.Equal(t, "auto", recallLastUpstreamThinkingType(c))

	// nil 不 panic
	rememberEntryThinkingType(nil, "x")
	rememberLastUpstreamThinkingType(nil, "y")
	assert.Equal(t, "", recallEntryThinkingType(nil))
	assert.Equal(t, "", recallLastUpstreamThinkingType(nil))
}

func TestGetRequestIDsFromContext(t *testing.T) {
	t.Run("with both IDs", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req-001")
		ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-001")
		reqID, clientReqID := getRequestIDsFromContext(ctx)
		assert.Equal(t, "req-001", reqID)
		assert.Equal(t, "client-001", clientReqID)
	})

	t.Run("nil context", func(t *testing.T) {
		reqID, clientReqID := getRequestIDsFromContext(nil)
		assert.Equal(t, "", reqID)
		assert.Equal(t, "", clientReqID)
	})

	t.Run("no IDs in context", func(t *testing.T) {
		reqID, clientReqID := getRequestIDsFromContext(context.Background())
		assert.Equal(t, "", reqID)
		assert.Equal(t, "", clientReqID)
	})
}

func TestDebugThinkingEnabled_DefaultOff(t *testing.T) {
	svc := &GatewayService{}
	assert.False(t, svc.debugThinkingEnabled(), "debugThinking must be off by default")

	svc.debugThinking.Store(true)
	assert.True(t, svc.debugThinkingEnabled(), "debugThinking must be on after Store(true)")

	svc.debugThinking.Store(false)
	assert.False(t, svc.debugThinkingEnabled(), "debugThinking must be off after Store(false)")
}

func TestParseDebugEnvBool_ThinkingEnv(t *testing.T) {
	assert.False(t, parseDebugEnvBool(""))
	assert.False(t, parseDebugEnvBool("false"))
	assert.False(t, parseDebugEnvBool("0"))
	assert.False(t, parseDebugEnvBool("off"))
	assert.True(t, parseDebugEnvBool("true"))
	assert.True(t, parseDebugEnvBool("1"))
	assert.True(t, parseDebugEnvBool("yes"))
	assert.True(t, parseDebugEnvBool("on"))
	assert.True(t, parseDebugEnvBool("TRUE"))
}
