package service

import (
	"context"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// gatewayThinkingDebugEnv 是控制 thinking 诊断日志的环境变量名。
// 默认关闭。关闭时不产生任何额外日志。
const gatewayThinkingDebugEnv = "GATEWAY_THINKING_DEBUG"

// thinkingDebugIncomingTypeKey 用于在 gin.Context 中缓存请求入口处的
// thinking.type，供 outgoing 阶段比较判断是否发生了转换。
const thinkingDebugIncomingTypeKey = "_thinking_debug_incoming_type"

// thinkingDebugIncomingModelKey 缓存请求入口处的 public_model，
// 供 outgoing 阶段记录 thinking_transform_rule。
const thinkingDebugIncomingModelKey = "_thinking_debug_incoming_model"

// thinkingDebugFields 是 thinking 诊断日志的脱敏字段集合。
// 仅记录枚举值和数值，不记录任何 thinking block 文本、messages 内容或凭证。
type thinkingDebugFields struct {
	event                    string
	requestID                string
	clientRequestID          string
	publicModel              string
	mappedModel              string
	accountID                int64
	accountName              string
	accountPlatform          string
	provider                 string // account.Type
	upstreamHost             string // 仅 hostname
	stream                   bool
	thinkingType             string
	hasBudgetTokens          bool
	budgetTokens             int64
	thinkingTransformApplied bool
	thinkingTransformRule    string
}

// logThinkingDebug 以结构化方式输出一条 thinking 诊断日志。
// 所有字段通过 zap.Field 传递，不拼接任何请求体内容。
// 当 GATEWAY_THINKING_DEBUG 未开启时，本函数不应被调用（由调用方守卫）。
func logThinkingDebug(ctx context.Context, f thinkingDebugFields) {
	l := logger.FromContext(ctx).With(zap.String("component", "service.gateway"))
	fields := []zap.Field{
		zap.String("event", f.event),
		zap.String("request_id", f.requestID),
		zap.String("client_request_id", f.clientRequestID),
		zap.String("public_model", f.publicModel),
		zap.String("mapped_model", f.mappedModel),
		zap.Int64("account_id", f.accountID),
		zap.String("account_name", f.accountName),
		zap.String("account_platform", f.accountPlatform),
		zap.String("provider", f.provider),
		zap.String("upstream_host", f.upstreamHost),
		zap.Bool("stream", f.stream),
		zap.String("thinking_type", f.thinkingType),
		zap.Bool("has_budget_tokens", f.hasBudgetTokens),
		zap.Int64("budget_tokens", f.budgetTokens),
		zap.Bool("thinking_transform_applied", f.thinkingTransformApplied),
		zap.String("thinking_transform_rule", f.thinkingTransformRule),
	}
	l.Info("gateway.thinking_debug", fields...)
}

// extractThinkingInfoFromBody 从请求体中提取 thinking_type / has_budget_tokens / budget_tokens。
// 仅读取顶层 thinking.type 和 thinking.budget_tokens，不触及 messages 或 thinking block 文本。
func extractThinkingInfoFromBody(body []byte) (thinkingType string, hasBudgetTokens bool, budgetTokens int64) {
	if len(body) == 0 {
		return "", false, 0
	}
	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.Exists() || !thinking.IsObject() {
		return "", false, 0
	}
	thinkingType = strings.TrimSpace(thinking.Get("type").String())
	budget := thinking.Get("budget_tokens")
	if budget.Exists() {
		hasBudgetTokens = true
		budgetTokens = budget.Int()
	}
	return thinkingType, hasBudgetTokens, budgetTokens
}

// extractUpstreamHost 从完整 URL 中提取 hostname，不记录 query、token 或完整 URL。
// 解析失败时返回空字符串，绝不抛出原始 URL。
func extractUpstreamHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// getRequestIDsFromContext 从 context 中提取 request_id 和 client_request_id。
func getRequestIDsFromContext(ctx context.Context) (requestID, clientRequestID string) {
	if ctx == nil {
		return "", ""
	}
	if v, ok := ctx.Value(ctxkey.RequestID).(string); ok {
		requestID = v
	}
	if v, ok := ctx.Value(ctxkey.ClientRequestID).(string); ok {
		clientRequestID = v
	}
	return requestID, clientRequestID
}

// inferThinkingTransformRule 根据 incoming/outgoing thinking.type 和 mappedModel
// 推断可能触发的转换规则名称。这是启发式推断，仅用于诊断，不修改任何转换行为。
//
// 已知的 thinking.type 转换路径（截至当前代码）：
//   - NormalizeAnthropicThinking: 入口校验，拒绝非法值；disabled/auto 移除 budget_tokens
//   - NormalizeChineseLLMThinking: MiniMax M 系列 enabled → adaptive
//   - sanitizeBedrockThinking: Bedrock 路径 enabled/adaptive → auto（仅 Bedrock）
//   - RectifyThinkingBudget: retry 路径，→ enabled（仅 anthropic-strict）
func inferThinkingTransformRule(incomingType, outgoingType, mappedModel string) string {
	if incomingType == outgoingType {
		return ""
	}
	modelLower := strings.ToLower(mappedModel)

	// MiniMax M 系列：enabled → adaptive
	if strings.HasPrefix(modelLower, "minimax-m") {
		if incomingType == "enabled" && outgoingType == "adaptive" {
			return "NormalizeChineseLLMThinking"
		}
	}

	// Bedrock 路径：enabled/adaptive → auto
	if outgoingType == "auto" && (incomingType == "enabled" || incomingType == "adaptive") {
		return "sanitizeBedrockThinking"
	}

	// Retry rectifier：→ enabled
	if outgoingType == "enabled" && incomingType != "enabled" && incomingType != "" {
		return "RectifyThinkingBudget"
	}

	// 入口校验移除了 budget_tokens 但 type 不变的情况不会进入这里（type 相同）。
	return "type_changed:" + incomingType + "->" + outgoingType
}

// rememberIncomingThinkingState 在 gin.Context 中缓存入口处的 thinking.type 和 public_model，
// 供后续 outgoing 阶段比较。仅在 thinking debug 开启时调用。
func rememberIncomingThinkingState(c *gin.Context, thinkingType, publicModel string) {
	if c == nil {
		return
	}
	c.Set(thinkingDebugIncomingTypeKey, thinkingType)
	c.Set(thinkingDebugIncomingModelKey, publicModel)
}

// recallIncomingThinkingType 从 gin.Context 中取出入口处缓存的 thinking.type。
func recallIncomingThinkingType(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(thinkingDebugIncomingTypeKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// accountIDOrZero 安全获取 account.ID，nil 返回 0。
func accountIDOrZero(a *Account) int64 {
	if a == nil {
		return 0
	}
	return a.ID
}

// accountNameOrEmpty 安全获取 account.Name，nil 返回 ""。
func accountNameOrEmpty(a *Account) string {
	if a == nil {
		return ""
	}
	return a.Name
}

// accountPlatformOrEmpty 安全获取 account.Platform，nil 返回 ""。
func accountPlatformOrEmpty(a *Account) string {
	if a == nil {
		return ""
	}
	return a.Platform
}

// accountTypeOrEmpty 安全获取 account.Type，nil 返回 ""。
func accountTypeOrEmpty(a *Account) string {
	if a == nil {
		return ""
	}
	return a.Type
}

// logThinkingOutgoing 是 gateway.thinking_outgoing 日志的共用构造方法。
// 在 buildUpstreamRequest（Anthropic 直连）和 buildUpstreamRequestAnthropicVertex
// （Vertex）返回前调用，覆盖流式与非流式。
//
// 参数：
//   - body: 最终发送给上游的请求体（已经过所有 sanitize / beta 对齐 / CCH 签名前处理）
//   - rawURL: 上游目标 URL（仅提取 hostname，不记录 query / token / 完整 URL）
//   - modelID: 映射后的模型 ID（即 mapped_model）
//   - reqStream: 是否为流式请求
func (s *GatewayService) logThinkingOutgoing(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	rawURL string,
	modelID string,
	reqStream bool,
) {
	outgoingType, hasBudget, budget := extractThinkingInfoFromBody(body)
	incomingType := recallIncomingThinkingType(c)
	transformApplied := incomingType != outgoingType
	transformRule := ""
	if transformApplied {
		transformRule = inferThinkingTransformRule(incomingType, outgoingType, modelID)
	}
	reqID, clientReqID := getRequestIDsFromContext(ctx)
	logThinkingDebug(ctx, thinkingDebugFields{
		event:                    "gateway.thinking_outgoing",
		requestID:                reqID,
		clientRequestID:          clientReqID,
		publicModel:              recallIncomingPublicModel(c),
		mappedModel:              modelID,
		accountID:                accountIDOrZero(account),
		accountName:              accountNameOrEmpty(account),
		accountPlatform:          accountPlatformOrEmpty(account),
		provider:                 accountTypeOrEmpty(account),
		upstreamHost:             extractUpstreamHost(rawURL),
		stream:                   reqStream,
		thinkingType:             outgoingType,
		hasBudgetTokens:          hasBudget,
		budgetTokens:             budget,
		thinkingTransformApplied: transformApplied,
		thinkingTransformRule:    transformRule,
	})
}

// recallIncomingPublicModel 从 gin.Context 中取出入口处缓存的 public_model。
func recallIncomingPublicModel(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(thinkingDebugIncomingModelKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
