package service

import (
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// ThinkingDiagnostics 是从请求体中安全提取的 thinking 字段元数据。
//
// 仅包含枚举/布尔/数值，绝不包含 thinking block 文本、messages 内容、
// system prompt、tools 定义或凭证。用于诊断日志，不参与请求转发决策。
type ThinkingDiagnostics struct {
	// Present 表示请求体顶层是否存在 thinking 字段（含 null）。
	Present bool
	// Type 是 thinking.type 的规范化字符串值（已 TrimSpace）。
	// 字段缺失 / 非 string / null 时为空字符串。
	Type string
	// BudgetTokensPresent 表示 thinking.budget_tokens 字段是否存在。
	BudgetTokensPresent bool
	// BudgetTokens 是 thinking.budget_tokens 的数值。
	// 仅在字段存在且为 JSON number 时有效；否则为 0。
	// 安全记录：budget_tokens 是合法数字型配置，不是敏感内容。
	BudgetTokens int64
}

// extractThinkingDiagnostics 从请求体中安全提取 thinking 元数据。
//
// 安全保证：
//   - 不修改 body
//   - 对空值、null、非对象、非法 JSON 安全返回零值
//   - 不记录敏感内容（messages / system prompt / tools / 凭证）
//   - 不返回错误（诊断逻辑绝不影响正常请求转发）
func extractThinkingDiagnostics(body []byte) ThinkingDiagnostics {
	var d ThinkingDiagnostics
	if len(body) == 0 {
		return d
	}
	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.Exists() {
		return d
	}
	d.Present = true
	if !thinking.IsObject() {
		// thinking 为 null / bool / number / string / array：仅记录 Present=true。
		return d
	}
	// 仅当 type 字段为 JSON string 时才记录，避免将 number/bool/null 强制转为字符串。
	typeResult := thinking.Get("type")
	if typeResult.Type == gjson.String {
		d.Type = strings.TrimSpace(typeResult.String())
	}
	budget := thinking.Get("budget_tokens")
	if budget.Exists() {
		d.BudgetTokensPresent = true
		if budget.Type == gjson.Number {
			d.BudgetTokens = budget.Int()
		}
	}
	return d
}

// thinkingForwardPath 枚举：用于日志的稳定字符串值。
const (
	forwardPathAnthropicPassthrough = "anthropic_passthrough"
	forwardPathAnthropicStandard    = "anthropic_standard"
	forwardPathBedrock              = "bedrock"
	forwardPathVertex               = "vertex"
	forwardPathCustomRelay          = "custom_relay"
	forwardPathUnknown              = "unknown"
)

// resolveForwardPath 根据账号属性推断转发路径的稳定枚举值。
// 仅依据账号平台/类型/配置，不读取请求体或敏感字段。
//
// 优先级：
//  1. anthropic_passthrough: account.IsAnthropicAPIKeyPassthroughEnabled()
//  2. bedrock: account.IsBedrock()
//  3. vertex: PlatformAnthropic + AccountTypeServiceAccount
//  4. custom_relay: account.IsCustomBaseURLEnabled()
//  5. anthropic_standard: 其他 Anthropic 平台账号
//  6. unknown: 账号为 nil 或非 Anthropic 平台
func resolveForwardPath(account *Account) string {
	if account == nil {
		return forwardPathUnknown
	}
	if account.IsAnthropicAPIKeyPassthroughEnabled() {
		return forwardPathAnthropicPassthrough
	}
	if account.IsBedrock() {
		return forwardPathBedrock
	}
	if account.Platform == PlatformAnthropic && account.Type == AccountTypeServiceAccount {
		return forwardPathVertex
	}
	if account.IsCustomBaseURLEnabled() {
		return forwardPathCustomRelay
	}
	if account.Platform == PlatformAnthropic {
		return forwardPathAnthropicStandard
	}
	return forwardPathUnknown
}

// thinkingProtocolName 枚举：用于日志的稳定字符串值。
const (
	thinkingProtocolAnthropicStrict  = "anthropic_strict"
	thinkingProtocolPassbackRequired = "passback_required"
	thinkingProtocolUnknown          = "unknown"
)

// resolveThinkingProtocolName 返回 ResolveThinkingProtocol 的稳定字符串名。
// 用于诊断日志，不参与转发决策。
func resolveThinkingProtocolName(mappedModel string) string {
	switch ResolveThinkingProtocol(mappedModel) {
	case ThinkingProtocolAnthropicStrict:
		return thinkingProtocolAnthropicStrict
	case ThinkingProtocolPassbackRequired:
		return thinkingProtocolPassbackRequired
	default:
		return thinkingProtocolUnknown
	}
}

// thinkingNormalizer 枚举：标识哪个 provider-specific 适配器对 thinking 做了改动。
const (
	normalizerDeepSeekV4 = "deepseek_v4"
	normalizerChineseLLM = "chinese_llm"
	normalizerBedrock    = "bedrock"
	normalizerSenseNova  = "sensenova"
	normalizerNone       = "none"
)

// extractUpstreamEndpointPath 从完整 URL 中提取 path 部分（不含 query / fragment / 用户信息）。
// 仅记录 endpoint path，不记录可能包含敏感查询参数的完整 URL。
// 解析失败时返回空字符串，绝不抛出原始 URL。
func extractUpstreamEndpointPath(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u == nil {
		return ""
	}
	return u.Path
}

// 注：账号类型的安全判断复用 gateway_thinking_debug.go 中已有的
// accountTypeOrEmpty / accountNameOrEmpty / accountPlatformOrEmpty 等 helper，
// 以及 Account.IsOAuth / IsBedrock / IsAnthropicAPIKeyPassthroughEnabled 等方法。
// 这些 helper 自身已处理 nil 入参，无需在此重复定义。
