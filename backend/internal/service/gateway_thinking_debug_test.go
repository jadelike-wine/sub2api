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
func thinkingDebugLoggedFields(t *testing.T, logs *observer.ObservedLogs) map[string]any {
	t.Helper()
	entries := logs.All()
	require.Len(t, entries, 1, "expected exactly one log entry")
	fields := map[string]any{}
	for _, f := range entries[0].Context {
		switch f.Key {
		case "account_id", "budget_tokens":
			fields[f.Key] = f.Integer
		case "stream", "has_budget_tokens", "thinking_transform_applied":
			// zap.Bool 内部存储为 Integer（0/1）
			fields[f.Key] = f.Integer == 1
		default:
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

func TestInferThinkingTransformRule(t *testing.T) {
	tests := []struct {
		name         string
		incoming     string
		outgoing     string
		mappedModel  string
		wantRule     string
		wantApplied  bool
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

func TestLogThinkingDebug_IncomingEvent(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)
	ctx = context.WithValue(ctx, ctxkey.RequestID, "req-123")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-456")

	body := `{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":32000},"stream":true}`
	incomingType, hasBudget, budget := extractThinkingInfoFromBody([]byte(body))

	logThinkingDebug(ctx, thinkingDebugFields{
		event:           "gateway.thinking_incoming",
		requestID:       "req-123",
		clientRequestID: "client-456",
		publicModel:     "claude-opus-5",
		mappedModel:     "claude-opus-5",
		accountID:       42,
		accountName:     "test-account",
		accountPlatform: "anthropic",
		provider:        "api_key",
		stream:          true,
		thinkingType:    incomingType,
		hasBudgetTokens: hasBudget,
		budgetTokens:    budget,
	})

	fields := thinkingDebugLoggedFields(t, logs)
	assert.Equal(t, "gateway.thinking_debug", fields["message"])
	assert.Equal(t, "gateway.thinking_incoming", fields["event"])
	assert.Equal(t, "req-123", fields["request_id"])
	assert.Equal(t, "client-456", fields["client_request_id"])
	assert.Equal(t, "claude-opus-5", fields["public_model"])
	assert.Equal(t, "claude-opus-5", fields["mapped_model"])
	assert.EqualValues(t, 42, fields["account_id"])
	assert.Equal(t, "test-account", fields["account_name"])
	assert.Equal(t, "anthropic", fields["account_platform"])
	assert.Equal(t, "api_key", fields["provider"])
	assert.Equal(t, "enabled", fields["thinking_type"])
	assert.Equal(t, true, fields["has_budget_tokens"])
	assert.EqualValues(t, 32000, fields["budget_tokens"])
	assert.Equal(t, true, fields["stream"])
}

func TestLogThinkingDebug_OutgoingEventWithTransform(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)

	logThinkingDebug(ctx, thinkingDebugFields{
		event:                    "gateway.thinking_outgoing",
		requestID:                "req-789",
		publicModel:              "claude-opus-5",
		mappedModel:              "MiniMax-M2",
		accountPlatform:          "anthropic",
		upstreamHost:             "api.minimax.io",
		stream:                   false,
		thinkingType:             "adaptive",
		thinkingTransformApplied: true,
		thinkingTransformRule:    "NormalizeChineseLLMThinking",
	})

	fields := thinkingDebugLoggedFields(t, logs)
	assert.Equal(t, "gateway.thinking_outgoing", fields["event"])
	assert.Equal(t, "MiniMax-M2", fields["mapped_model"])
	assert.Equal(t, "api.minimax.io", fields["upstream_host"])
	assert.Equal(t, "adaptive", fields["thinking_type"])
	assert.Equal(t, true, fields["thinking_transform_applied"])
	assert.Equal(t, "NormalizeChineseLLMThinking", fields["thinking_transform_rule"])
}

func TestLogThinkingDebug_NoSensitiveData(t *testing.T) {
	observedLog, logs := newThinkingDebugObservedLogger(t)
	ctx := logger.IntoContext(context.Background(), observedLog)

	// 确保日志中不包含任何敏感字段。
	// 以下 body 仅用于构造测试场景，不会传入 logThinkingDebug。
	body := `{"model":"claude-opus-5","authorization":"Bearer sk-secret","messages":[{"role":"user","content":"secret user text"}],"thinking":{"type":"enabled","budget_tokens":1000}}`
	incomingType, hasBudget, budget := extractThinkingInfoFromBody([]byte(body))

	logThinkingDebug(ctx, thinkingDebugFields{
		event:           "gateway.thinking_incoming",
		thinkingType:    incomingType,
		hasBudgetTokens: hasBudget,
		budgetTokens:    budget,
	})

	fields := thinkingDebugLoggedFields(t, logs)
	// 确认仅记录 thinking_type 和 budget_tokens 数值，不记录 messages / authorization / 用户文本
	for key, val := range fields {
		if s, ok := val.(string); ok {
			assert.NotContains(t, s, "sk-secret", "field %s must not contain API key", key)
			assert.NotContains(t, s, "secret user text", "field %s must not contain user text", key)
			assert.NotContains(t, s, "Bearer", "field %s must not contain auth header", key)
		}
	}
	// 确认 budget_tokens 只记录了数值
	assert.EqualValues(t, 1000, fields["budget_tokens"])
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
	// GatewayService 默认 debugThinking=false（未调用 Store(true)）
	svc := &GatewayService{}
	assert.False(t, svc.debugThinkingEnabled(), "debugThinking must be off by default")

	svc.debugThinking.Store(true)
	assert.True(t, svc.debugThinkingEnabled(), "debugThinking must be on after Store(true)")

	svc.debugThinking.Store(false)
	assert.False(t, svc.debugThinkingEnabled(), "debugThinking must be off after Store(false)")
}

func TestParseDebugEnvBool_ThinkingEnv(t *testing.T) {
	// 验证 GATEWAY_THINKING_DEBUG 环境变量的解析语义
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
