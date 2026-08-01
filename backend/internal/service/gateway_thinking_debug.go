package service

import (
	"context"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// gatewayThinkingDebugEnv 是控制 thinking 诊断日志的环境变量名。
// 默认关闭。关闭时不产生任何额外日志（上游错误日志除外，见
// logThinkingUpstreamError）。
const gatewayThinkingDebugEnv = "GATEWAY_THINKING_DEBUG"

// thinkingDebugIncomingTypeKey 用于在 gin.Context 中缓存请求入口处的
// thinking.type（NormalizeAnthropicThinking 之前的原始值），供后续阶段比较。
const thinkingDebugIncomingTypeKey = "_thinking_debug_incoming_type"

// thinkingDebugIncomingModelKey 缓存请求入口处的 public_model，
// 供后续阶段记录 requested_model / thinking_transform_rule。
const thinkingDebugIncomingModelKey = "_thinking_debug_incoming_model"

// thinkingDebugEntryTypeKey 缓存 NormalizeAnthropicThinking 之后的 thinking.type，
// 供 provider_normalized / upstream_ready 阶段比较。
const thinkingDebugEntryTypeKey = "_thinking_debug_entry_type"

// thinkingDebugLastUpstreamTypeKey 缓存最后一次 provider 适配后的 thinking.type，
// 供 upstream_ready / upstream_error 阶段读取 thinking_type_before_upstream。
const thinkingDebugLastUpstreamTypeKey = "_thinking_debug_last_upstream_type"

// thinkingDebugFields 是 thinking 诊断日志的脱敏字段集合。
//
// 仅记录枚举值、布尔值、数值、模型名称、账号 ID、请求 ID 和 thinking 配置元数据。
// 严禁记录完整请求 body、messages 内容、system prompt、tools 定义、tool input、
// Authorization / x-api-key / Cookie / 完整请求头 / thinking block 推理内容 / signature。
type thinkingDebugFields struct {
	// === 事件标识 ===
	event string

	// === 请求基础信息 ===
	requestID       string
	clientRequestID string
	accountID       int64
	accountName     string
	accountType     string // = account.Type
	accountPlatform string
	requestedModel  string // 客户端原始请求模型
	mappedModel     string // 模型映射后的上游模型
	stream          bool

	// === Thinking 信息 ===
	thinkingPresent             bool
	thinkingTypeInboundRaw      string // 客户端原始 thinking.type（NormalizeAnthropicThinking 前）
	thinkingTypeAfterEntryNorm  string // 入口标准化后的 thinking.type
	thinkingTypeBeforeProvider  string // provider 适配前的 thinking.type
	thinkingTypeAfterProvider   string // provider 适配后的 thinking.type
	thinkingTypeBeforeUpstream  string // 上游发送前的 thinking.type（从最终 body 重新读取）
	thinkingBudgetTokensPresent bool
	budgetTokens                int64
	thinkingProtocol            string // anthropic_strict / passback_required / unknown
	isDeepSeekV4Model           bool

	// === 转换诊断 ===
	thinkingChanged          bool   // 当前阶段 thinking.type 是否发生变化
	thinkingTransformApplied bool   // 整条链路是否发生转换（incoming vs upstream）
	thinkingTransformRule    string // 启发式推断的转换规则名
	normalizer               string // provider_normalized 阶段触发的适配器名

	// === 转发路径信息 ===
	anthropicPassthroughEnabled bool
	isBedrock                   bool
	isVertex                    bool
	isOAuth                     bool
	isAPIKey                    bool
	forwardPath                 string // anthropic_passthrough / anthropic_standard / bedrock / vertex / custom_relay / unknown

	// === 上游端点（仅记录 hostname / path，不记录 query）===
	upstreamHost         string
	upstreamEndpointPath string
}

// logThinkingDebug 以结构化方式输出一条 thinking 诊断日志。
// 所有字段通过 zap.Field 传递，不拼接任何请求体内容。
//
// 调用方守卫：当 GATEWAY_THINKING_DEBUG 未开启时，除 logThinkingUpstreamError
// 外不应调用本函数。
func logThinkingDebug(ctx context.Context, f thinkingDebugFields) {
	l := logger.FromContext(ctx).With(zap.String("component", "service.gateway"))
	fields := []zap.Field{
		zap.String("event", f.event),
		zap.String("request_id", f.requestID),
		zap.String("client_request_id", f.clientRequestID),
		zap.Int64("account_id", f.accountID),
		zap.String("account_name", f.accountName),
		zap.String("account_type", f.accountType),
		zap.String("account_platform", f.accountPlatform),
		zap.String("requested_model", f.requestedModel),
		zap.String("mapped_model", f.mappedModel),
		zap.Bool("stream", f.stream),
		zap.Bool("thinking_present", f.thinkingPresent),
		zap.String("thinking_type_inbound_raw", f.thinkingTypeInboundRaw),
		zap.String("thinking_type_after_entry_normalize", f.thinkingTypeAfterEntryNorm),
		zap.String("thinking_type_before_provider_normalize", f.thinkingTypeBeforeProvider),
		zap.String("thinking_type_after_provider_normalize", f.thinkingTypeAfterProvider),
		zap.String("thinking_type_before_upstream", f.thinkingTypeBeforeUpstream),
		zap.Bool("thinking_budget_tokens_present", f.thinkingBudgetTokensPresent),
		zap.Int64("budget_tokens", f.budgetTokens),
		zap.String("thinking_protocol", f.thinkingProtocol),
		zap.Bool("is_deepseek_v4_model", f.isDeepSeekV4Model),
		zap.Bool("thinking_changed", f.thinkingChanged),
		zap.Bool("thinking_transform_applied", f.thinkingTransformApplied),
		zap.String("thinking_transform_rule", f.thinkingTransformRule),
		zap.String("normalizer", f.normalizer),
		zap.Bool("anthropic_passthrough_enabled", f.anthropicPassthroughEnabled),
		zap.Bool("is_bedrock", f.isBedrock),
		zap.Bool("is_vertex", f.isVertex),
		zap.Bool("is_oauth", f.isOAuth),
		zap.Bool("is_api_key", f.isAPIKey),
		zap.String("forward_path", f.forwardPath),
		zap.String("upstream_host", f.upstreamHost),
		zap.String("upstream_endpoint_path", f.upstreamEndpointPath),
	}
	l.Info("gateway.thinking_debug", fields...)
}

// logThinkingUpstreamError 在上游返回错误时输出关键 thinking 诊断字段。
//
// 与 logThinkingDebug 不同，本函数**不受 GATEWAY_THINKING_DEBUG 开关控制**：
// 只要请求中存在 thinking 字段（或开关开启），上游错误就一定伴随这些字段，
// 便于运维在不开启动全量诊断日志的情况下也能定位 thinking 链路问题。
//
// 仅记录枚举/布尔/数值/模型名/账号 ID/请求 ID/thinking 配置元数据，
// 不记录完整 body / messages / API Key / Authorization / system prompt。
func logThinkingUpstreamError(ctx context.Context, f thinkingDebugFields) {
	l := logger.FromContext(ctx).With(zap.String("component", "service.gateway"))
	l.Error("gateway.thinking.upstream_error",
		zap.String("event", "gateway.thinking.upstream_error"),
		zap.String("request_id", f.requestID),
		zap.String("client_request_id", f.clientRequestID),
		zap.Int64("account_id", f.accountID),
		zap.String("account_name", f.accountName),
		zap.String("requested_model", f.requestedModel),
		zap.String("mapped_model", f.mappedModel),
		zap.String("forward_path", f.forwardPath),
		zap.Bool("anthropic_passthrough_enabled", f.anthropicPassthroughEnabled),
		zap.String("thinking_type_inbound_raw", f.thinkingTypeInboundRaw),
		zap.String("thinking_type_before_upstream", f.thinkingTypeBeforeUpstream),
		zap.String("thinking_protocol", f.thinkingProtocol),
		zap.Bool("thinking_present", f.thinkingPresent),
		zap.Bool("is_deepseek_v4_model", f.isDeepSeekV4Model),
	)
}

// extractThinkingInfoFromBody 从请求体中提取 thinking_type / has_budget_tokens / budget_tokens。
// 仅读取顶层 thinking.type 和 thinking.budget_tokens，不触及 messages 或 thinking block 文本。
//
// 保留为 legacy helper（既有测试依赖）。新代码应优先使用 extractThinkingDiagnostics。
func extractThinkingInfoFromBody(body []byte) (thinkingType string, hasBudgetTokens bool, budgetTokens int64) {
	d := extractThinkingDiagnostics(body)
	return d.Type, d.BudgetTokensPresent, d.BudgetTokens
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
//   - NormalizeDeepSeekV4Thinking: DeepSeek V4 系列 auto → adaptive
//   - sanitizeBedrockThinking: Bedrock 路径 enabled/adaptive → auto（仅 Bedrock）
//   - RectifyThinkingBudget: retry 路径，→ enabled（仅 anthropic-strict）
func inferThinkingTransformRule(incomingType, outgoingType, mappedModel string) string {
	if incomingType == outgoingType {
		return ""
	}
	modelLower := strings.ToLower(mappedModel)

	// DeepSeek V4：auto → adaptive
	if strings.HasPrefix(modelLower, "deepseek-v4") {
		if incomingType == "auto" && outgoingType == "adaptive" {
			return "NormalizeDeepSeekV4Thinking"
		}
	}

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

// rememberIncomingThinkingState 在 gin.Context 中缓存入口处的
// thinking.type（NormalizeAnthropicThinking 之前的原始值）和 public_model，
// 供后续阶段比较。仅在 thinking debug 开启时调用。
func rememberIncomingThinkingState(c *gin.Context, thinkingType, publicModel string) {
	if c == nil {
		return
	}
	c.Set(thinkingDebugIncomingTypeKey, thinkingType)
	c.Set(thinkingDebugIncomingModelKey, publicModel)
}

// rememberEntryThinkingType 缓存 NormalizeAnthropicThinking 之后的 thinking.type，
// 供 provider_normalized / upstream_ready 阶段比较。
func rememberEntryThinkingType(c *gin.Context, entryType string) {
	if c == nil {
		return
	}
	c.Set(thinkingDebugEntryTypeKey, entryType)
}

// rememberLastUpstreamThinkingType 缓存 provider 适配后的 thinking.type，
// 供 upstream_ready / upstream_error 阶段读取 thinking_type_before_upstream。
func rememberLastUpstreamThinkingType(c *gin.Context, t string) {
	if c == nil {
		return
	}
	c.Set(thinkingDebugLastUpstreamTypeKey, t)
}

// recallIncomingThinkingType 从 gin.Context 中取出入口处缓存的原始 thinking.type。
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

// recallEntryThinkingType 从 gin.Context 中取出 NormalizeAnthropicThinking 之后的 thinking.type。
func recallEntryThinkingType(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(thinkingDebugEntryTypeKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// recallLastUpstreamThinkingType 从 gin.Context 中取出 provider 适配后的 thinking.type。
func recallLastUpstreamThinkingType(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(thinkingDebugLastUpstreamTypeKey); ok {
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

// logThinkingInbound 记录客户端原始请求中的 thinking.type（NormalizeAnthropicThinking 前）。
//
// 事件名：gateway.thinking.inbound
// 触发点：ParseGatewayRequest 完成初始解析、尚未/刚调用 NormalizeAnthropicThinking 时。
// 守卫：GATEWAY_THINKING_DEBUG=true。
func (s *GatewayService) logThinkingInbound(
	ctx context.Context,
	account *Account,
	inboundDiagnostics ThinkingDiagnostics,
	requestedModel string,
	stream bool,
) {
	reqID, clientReqID := getRequestIDsFromContext(ctx)
	logThinkingDebug(ctx, thinkingDebugFields{
		event:                       "gateway.thinking.inbound",
		requestID:                   reqID,
		clientRequestID:             clientReqID,
		accountID:                   accountIDOrZero(account),
		accountName:                 accountNameOrEmpty(account),
		accountType:                 accountTypeOrEmpty(account),
		accountPlatform:             accountPlatformOrEmpty(account),
		requestedModel:              requestedModel,
		mappedModel:                 requestedModel, // 入口阶段尚未映射
		stream:                      stream,
		thinkingPresent:             inboundDiagnostics.Present,
		thinkingTypeInboundRaw:      inboundDiagnostics.Type,
		thinkingBudgetTokensPresent: inboundDiagnostics.BudgetTokensPresent,
		budgetTokens:                inboundDiagnostics.BudgetTokens,
		forwardPath:                 resolveForwardPath(account),
		anthropicPassthroughEnabled: account != nil && account.IsAnthropicAPIKeyPassthroughEnabled(),
		isBedrock:                   isBedrockAccountSafe(account),
		isVertex:                    isVertexAccountSafe(account),
		isOAuth:                     account != nil && account.IsOAuth(),
		isAPIKey:                    account != nil && account.Type == AccountTypeAPIKey,
	})
}

// logThinkingEntryNormalized 记录 NormalizeAnthropicThinking 完成后的 thinking.type。
//
// 事件名：gateway.thinking.entry_normalized
// 触发点：ParseGatewayRequest 中 NormalizeAnthropicThinking 执行完毕后（在 Forward 入口处发射日志）。
// 守卫：GATEWAY_THINKING_DEBUG=true。
func (s *GatewayService) logThinkingEntryNormalized(
	ctx context.Context,
	account *Account,
	inboundType string,
	entryDiagnostics ThinkingDiagnostics,
	requestedModel string,
	stream bool,
) {
	reqID, clientReqID := getRequestIDsFromContext(ctx)
	changed := inboundType != entryDiagnostics.Type
	logThinkingDebug(ctx, thinkingDebugFields{
		event:                       "gateway.thinking.entry_normalized",
		requestID:                   reqID,
		clientRequestID:             clientReqID,
		accountID:                   accountIDOrZero(account),
		accountName:                 accountNameOrEmpty(account),
		accountType:                 accountTypeOrEmpty(account),
		accountPlatform:             accountPlatformOrEmpty(account),
		requestedModel:              requestedModel,
		mappedModel:                 requestedModel,
		stream:                      stream,
		thinkingPresent:             entryDiagnostics.Present,
		thinkingTypeInboundRaw:      inboundType,
		thinkingTypeAfterEntryNorm:  entryDiagnostics.Type,
		thinkingChanged:             changed,
		thinkingBudgetTokensPresent: entryDiagnostics.BudgetTokensPresent,
		budgetTokens:                entryDiagnostics.BudgetTokens,
		forwardPath:                 resolveForwardPath(account),
		anthropicPassthroughEnabled: account != nil && account.IsAnthropicAPIKeyPassthroughEnabled(),
		isBedrock:                   isBedrockAccountSafe(account),
		isVertex:                    isVertexAccountSafe(account),
		isOAuth:                     account != nil && account.IsOAuth(),
		isAPIKey:                    account != nil && account.Type == AccountTypeAPIKey,
	})
}

// logThinkingRouteResolved 记录账号选择 + 模型映射完成后的路由信息。
//
// 事件名：gateway.thinking.route_resolved
// 触发点：Forward() 中模型映射完成、转发路径已确定后。
// 守卫：GATEWAY_THINKING_DEBUG=true。
func (s *GatewayService) logThinkingRouteResolved(
	ctx context.Context,
	account *Account,
	requestedModel, mappedModel string,
	entryType string,
	stream bool,
) {
	reqID, clientReqID := getRequestIDsFromContext(ctx)
	logThinkingDebug(ctx, thinkingDebugFields{
		event:                       "gateway.thinking.route_resolved",
		requestID:                   reqID,
		clientRequestID:             clientReqID,
		accountID:                   accountIDOrZero(account),
		accountName:                 accountNameOrEmpty(account),
		accountType:                 accountTypeOrEmpty(account),
		accountPlatform:             accountPlatformOrEmpty(account),
		requestedModel:              requestedModel,
		mappedModel:                 mappedModel,
		stream:                      stream,
		thinkingTypeAfterEntryNorm:  entryType,
		thinkingProtocol:            resolveThinkingProtocolName(mappedModel),
		isDeepSeekV4Model:           isDeepSeekV4Model(mappedModel),
		forwardPath:                 resolveForwardPath(account),
		anthropicPassthroughEnabled: account != nil && account.IsAnthropicAPIKeyPassthroughEnabled(),
		isBedrock:                   isBedrockAccountSafe(account),
		isVertex:                    isVertexAccountSafe(account),
		isOAuth:                     account != nil && account.IsOAuth(),
		isAPIKey:                    account != nil && account.Type == AccountTypeAPIKey,
	})
}

// logThinkingProviderNormalized 记录 provider-specific thinking 适配器执行前后的 thinking.type。
//
// 事件名：gateway.thinking.provider_normalized
// 触发点：NormalizeChineseLLMThinking / NormalizeDeepSeekV4Thinking / sanitizeBedrockThinking 等执行前后。
// 守卫：GATEWAY_THINKING_DEBUG=true。
//
// 参数：
//   - beforeType: 适配器执行前的 thinking.type（从 body 重新读取）
//   - afterType: 适配器执行后的 thinking.type（从改写后的 body 重新读取）
//   - normalizer: deepseek_v4 / chinese_llm / bedrock / none
func (s *GatewayService) logThinkingProviderNormalized(
	ctx context.Context,
	account *Account,
	requestedModel, mappedModel string,
	beforeType, afterType, normalizer string,
	stream bool,
) {
	reqID, clientReqID := getRequestIDsFromContext(ctx)
	changed := beforeType != afterType
	logThinkingDebug(ctx, thinkingDebugFields{
		event:                       "gateway.thinking.provider_normalized",
		requestID:                   reqID,
		clientRequestID:             clientReqID,
		accountID:                   accountIDOrZero(account),
		accountName:                 accountNameOrEmpty(account),
		accountType:                 accountTypeOrEmpty(account),
		accountPlatform:             accountPlatformOrEmpty(account),
		requestedModel:              requestedModel,
		mappedModel:                 mappedModel,
		stream:                      stream,
		thinkingTypeBeforeProvider:  beforeType,
		thinkingTypeAfterProvider:   afterType,
		thinkingChanged:             changed,
		normalizer:                  normalizer,
		thinkingProtocol:            resolveThinkingProtocolName(mappedModel),
		isDeepSeekV4Model:           isDeepSeekV4Model(mappedModel),
		forwardPath:                 resolveForwardPath(account),
		anthropicPassthroughEnabled: account != nil && account.IsAnthropicAPIKeyPassthroughEnabled(),
		isBedrock:                   isBedrockAccountSafe(account),
		isVertex:                    isVertexAccountSafe(account),
		isOAuth:                     account != nil && account.IsOAuth(),
		isAPIKey:                    account != nil && account.Type == AccountTypeAPIKey,
	})
}

// logThinkingUpstreamReady 记录最终发送给上游的 thinking.type。
//
// 事件名：gateway.thinking.upstream_ready
// 触发点：buildUpstreamRequest / buildUpstreamRequestAnthropicVertex /
//
//	buildUpstreamRequestAnthropicAPIKeyPassthrough / buildUpstreamRequestBedrock
//	返回前（HTTP 请求已构造完成、即将调用上游）。
//
// 守卫：GATEWAY_THINKING_DEBUG=true。
//
// 关键：thinkingTypeBeforeUpstream 必须从最终发送的 body 重新读取，
// 不能只复用之前的变量，否则无法发现中间某一步又修改了请求体。
func (s *GatewayService) logThinkingUpstreamReady(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	finalBody []byte,
	rawURL string,
	requestedModel, mappedModel string,
	stream bool,
) {
	finalDiag := extractThinkingDiagnostics(finalBody)
	incomingType := recallIncomingThinkingType(c)
	entryType := recallEntryThinkingType(c)
	lastUpstreamType := recallLastUpstreamThinkingType(c)
	transformApplied := incomingType != finalDiag.Type
	transformRule := ""
	if transformApplied {
		transformRule = inferThinkingTransformRule(incomingType, finalDiag.Type, mappedModel)
	}
	reqID, clientReqID := getRequestIDsFromContext(ctx)
	logThinkingDebug(ctx, thinkingDebugFields{
		event:                       "gateway.thinking.upstream_ready",
		requestID:                   reqID,
		clientRequestID:             clientReqID,
		accountID:                   accountIDOrZero(account),
		accountName:                 accountNameOrEmpty(account),
		accountType:                 accountTypeOrEmpty(account),
		accountPlatform:             accountPlatformOrEmpty(account),
		requestedModel:              requestedModel,
		mappedModel:                 mappedModel,
		stream:                      stream,
		thinkingPresent:             finalDiag.Present,
		thinkingTypeInboundRaw:      incomingType,
		thinkingTypeAfterEntryNorm:  entryType,
		thinkingTypeBeforeProvider:  lastUpstreamType,
		thinkingTypeAfterProvider:   lastUpstreamType,
		thinkingTypeBeforeUpstream:  finalDiag.Type,
		thinkingBudgetTokensPresent: finalDiag.BudgetTokensPresent,
		budgetTokens:                finalDiag.BudgetTokens,
		thinkingTransformApplied:    transformApplied,
		thinkingTransformRule:       transformRule,
		thinkingProtocol:            resolveThinkingProtocolName(mappedModel),
		isDeepSeekV4Model:           isDeepSeekV4Model(mappedModel),
		forwardPath:                 resolveForwardPath(account),
		anthropicPassthroughEnabled: account != nil && account.IsAnthropicAPIKeyPassthroughEnabled(),
		isBedrock:                   isBedrockAccountSafe(account),
		isVertex:                    isVertexAccountSafe(account),
		isOAuth:                     account != nil && account.IsOAuth(),
		isAPIKey:                    account != nil && account.Type == AccountTypeAPIKey,
		upstreamHost:                extractUpstreamHost(rawURL),
		upstreamEndpointPath:        extractUpstreamEndpointPath(rawURL),
	})
}

// emitThinkingUpstreamErrorOnFailure 在上游返回错误时发射关键 thinking 诊断字段。
//
// 守卫：仅当请求包含 thinking 字段，或 GATEWAY_THINKING_DEBUG 开启时才发射。
// 这样即使未开启全量诊断日志，发生 thinking 相关的上游错误也能看到关键链路字段。
//
// 调用点：gateway_forward.go / gateway_anthropic_passthrough.go / gateway_bedrock.go
// 中上游返回 >= 400 错误的路径。
func (s *GatewayService) emitThinkingUpstreamErrorOnFailure(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	requestedModel, mappedModel string,
) {
	if s == nil {
		return
	}
	// 仅当请求存在 thinking 或诊断开关开启时才记录，避免给所有错误请求增加日志噪声。
	if !s.debugThinkingEnabled() && !requestHadThinking(c) {
		return
	}
	incomingType := recallIncomingThinkingType(c)
	beforeUpstream := recallLastUpstreamThinkingType(c)
	// fallback：若 provider 阶段未缓存（如 passthrough 路径），从当前 body 重读。
	if beforeUpstream == "" && c != nil {
		if pr, ok := c.Get("_parsed_request_ref"); ok {
			if parsed, ok := pr.(*ParsedRequest); ok && parsed != nil && parsed.Body != nil {
				beforeUpstream = extractThinkingDiagnostics(parsed.Body.Bytes()).Type
			}
		}
	}
	reqID, clientReqID := getRequestIDsFromContext(ctx)
	logThinkingUpstreamError(ctx, thinkingDebugFields{
		requestID:                   reqID,
		clientRequestID:             clientReqID,
		accountID:                   accountIDOrZero(account),
		accountName:                 accountNameOrEmpty(account),
		requestedModel:              requestedModel,
		mappedModel:                 mappedModel,
		forwardPath:                 resolveForwardPath(account),
		anthropicPassthroughEnabled: account != nil && account.IsAnthropicAPIKeyPassthroughEnabled(),
		thinkingTypeInboundRaw:      incomingType,
		thinkingTypeBeforeUpstream:  beforeUpstream,
		thinkingProtocol:            resolveThinkingProtocolName(mappedModel),
		thinkingPresent:             incomingType != "" || beforeUpstream != "",
		isDeepSeekV4Model:           isDeepSeekV4Model(mappedModel),
	})
}

// requestHadThinking 通过 gin.Context 中缓存的入口 thinking.type 判断请求是否曾携带 thinking。
// 用于决定上游错误日志是否附带 thinking 诊断字段（避免给无 thinking 的请求增加噪声）。
func requestHadThinking(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return recallIncomingThinkingType(c) != "" || recallEntryThinkingType(c) != ""
}

// isBedrockAccountSafe 安全判断 account 是否为 Bedrock 类型，nil 返回 false。
func isBedrockAccountSafe(a *Account) bool {
	if a == nil {
		return false
	}
	return a.IsBedrock()
}

// isVertexAccountSafe 安全判断 account 是否为 Vertex (Anthropic ServiceAccount) 类型。
func isVertexAccountSafe(a *Account) bool {
	if a == nil {
		return false
	}
	return a.Platform == PlatformAnthropic && a.Type == AccountTypeServiceAccount
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
