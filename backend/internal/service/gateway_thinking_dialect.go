package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// ThinkingDialect 标识 thinking 字段的目标上游协议方言。
//
// 不同上游对 Anthropic thinking.type 的接受值不同：
//   - NativeDeepSeek (api.deepseek.com): 接受 enabled | disabled | adaptive，不接受 auto。
//   - SenseNova (token.sensenova.cn): 接受 enabled | disabled | auto，不接受 adaptive。
//   - Unknown: 未知第三方上游，保持原有行为（按 NativeDeepSeek 规则处理）。
type ThinkingDialect string

const (
	ThinkingDialectNativeDeepSeek ThinkingDialect = "native_deepseek"
	ThinkingDialectSenseNova      ThinkingDialect = "sensenova"
	ThinkingDialectUnknown        ThinkingDialect = "unknown"
)

// sensenovaHost 是 SenseNova 上游的确切 hostname。
const sensenovaHost = "token.sensenova.cn"

// nativeDeepSeekHost 是原生 DeepSeek API 的确切 hostname。
const nativeDeepSeekHost = "api.deepseek.com"

// isSenseNovaUpstream 判断 Base URL 是否指向 SenseNova 上游。
//
// 通过解析 URL 的 hostname 进行精确匹配（不比较完整 URL），
// 兼容以下形式：
//   - https://token.sensenova.cn
//   - https://token.sensenova.cn/
//   - https://token.sensenova.cn/v1
//   - https://token.sensenova.cn:443
//
// 不会匹配相似域名：
//   - token.sensenova.cn.example.com
//   - evil-token.sensenova.cn
//   - sensenova.cn
func isSenseNovaUpstream(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), sensenovaHost)
}

// isNativeDeepSeekUpstream 判断 Base URL 是否指向原生 DeepSeek API。
func isNativeDeepSeekUpstream(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), nativeDeepSeekHost)
}

// resolveUpstreamBaseURL 从账号解析上游 Base URL。
//
// 对于 Anthropic 平台 APIKey 账号，使用 GetBaseURL()。
// 对于 OpenAI 平台 APIKey 账号（CC 直转路径），使用 GetOpenAIBaseURL()。
// 其他情况返回空字符串。
func resolveUpstreamBaseURL(account *Account) string {
	if account == nil {
		return ""
	}
	if account.IsOpenAI() {
		return account.GetOpenAIBaseURL()
	}
	if account.Type == AccountTypeAPIKey {
		return account.GetBaseURL()
	}
	return ""
}

// ResolveThinkingDialect 根据账号和映射模型解析 thinking 协议方言。
//
// 判断原则：
//  1. 映射模型不属于 deepseek-v4 系列 → Unknown（不需要 thinking 适配）
//  2. hostname == token.sensenova.cn → SenseNova
//  3. hostname == api.deepseek.com → NativeDeepSeek
//  4. 其他上游 → Unknown（保持修复前安全行为，即按原生 DeepSeek 规则处理）
//
// 不会因为 Base URL 不是 api.deepseek.com 就强制按 SenseNova 处理，
// 避免破坏其他支持 adaptive 的第三方网关。
func ResolveThinkingDialect(account *Account, mappedModel string) ThinkingDialect {
	if !isDeepSeekV4Model(mappedModel) {
		return ThinkingDialectUnknown
	}

	baseURL := resolveUpstreamBaseURL(account)
	switch {
	case isSenseNovaUpstream(baseURL):
		return ThinkingDialectSenseNova
	case isNativeDeepSeekUpstream(baseURL):
		return ThinkingDialectNativeDeepSeek
	default:
		// 未知第三方上游 + deepseek-v4 模型。
		// 保持修复前行为：按原生 DeepSeek V4 规则处理。
		// 不对未知上游强制执行 SenseNova 的 adaptive → auto 转换，
		// 避免破坏支持 adaptive 的第三方网关。
		return ThinkingDialectUnknown
	}
}

// NormalizeSenseNovaThinking 将 thinking 字段适配为 SenseNova 上游契约。
//
// SenseNova 的 Anthropic 兼容网关仅接受 thinking.type = enabled | disabled | auto，
// 不接受 adaptive（返回 400 "'type' must be in [\"enabled\", \"disabled\", \"auto\"]"）。
//
// 转换规则：
//   - 未传 thinking → 不生成 thinking 字段。
//   - thinking.type=adaptive → auto（SenseNova 不接受 adaptive），并删除 budget_tokens。
//   - thinking.type=auto → 保留 auto，删除 budget_tokens（auto 模式不允许）。
//   - thinking.type=enabled → 保留 enabled + budget_tokens（不变）。
//   - thinking.type=disabled → 保留 disabled，删除 budget_tokens（disabled 模式不允许）。
//   - thinking.type 为其他字符串 → 返回错误（遵循项目现有错误处理方式，不静默猜测）。
//   - thinking 非对象或 type 非字符串 → 返回错误。
//
// 注意：此函数不处理 output_config.effort → reasoning_effort 转换。
// output_config.effort 是 DeepSeek V4 原生 API 特有字段，SenseNova 作为
// Anthropic 兼容网关不适用该转换。
//
// 并发安全：不修改入参 body 切片，所有改动通过 sjson 返回新切片。
func NormalizeSenseNovaThinking(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.Exists() {
		return body, nil
	}

	if !thinking.IsObject() {
		return body, fmt.Errorf("unsupported thinking for SenseNova; expected an object")
	}

	typeRes := thinking.Get("type")
	if !typeRes.Exists() || typeRes.Type != gjson.String {
		return body, fmt.Errorf("unsupported thinking.type for SenseNova; expected enabled, disabled, or auto")
	}

	t := typeRes.String()
	switch t {
	case "adaptive":
		// adaptive → auto（SenseNova 不接受 adaptive），并删除 budget_tokens。
		body, _ = sjson.SetBytes(body, "thinking.type", "auto")
		body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
	case "auto":
		// 保留 auto，删除 budget_tokens（auto 模式不允许）。
		if thinking.Get("budget_tokens").Exists() {
			body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
		}
	case "enabled":
		// 保留 enabled + budget_tokens（不变）。
	case "disabled":
		// 保留 disabled，删除 budget_tokens（disabled 模式不允许）。
		if thinking.Get("budget_tokens").Exists() {
			body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
		}
	default:
		return body, fmt.Errorf("unsupported thinking.type %q for SenseNova; expected enabled, disabled, or auto", t)
	}

	return body, nil
}

// NormalizeDeepSeekV4ThinkingForAccount 是 thinking 协议适配的统一入口。
//
// 根据账号上游和映射模型决定使用哪种转换规则：
//   - SenseNova 上游 → NormalizeSenseNovaThinking
//   - NativeDeepSeek 上游 → NormalizeDeepSeekV4Thinking（原有行为不变）
//   - Unknown 上游 + deepseek-v4 模型 → NormalizeDeepSeekV4Thinking（保持修复前安全行为）
//   - 非 deepseek-v4 模型 → 不做转换
//
// 返回值：
//   - newBody: 转换后的 body
//   - dialect: 实际使用的方言（用于日志）
//   - err: 转换错误
func NormalizeDeepSeekV4ThinkingForAccount(account *Account, mappedModel string, body []byte) ([]byte, ThinkingDialect, error) {
	dialect := ResolveThinkingDialect(account, mappedModel)
	switch dialect {
	case ThinkingDialectSenseNova:
		rewritten, err := NormalizeSenseNovaThinking(body)
		return rewritten, dialect, err
	case ThinkingDialectNativeDeepSeek:
		rewritten, err := NormalizeDeepSeekV4Thinking(body)
		return rewritten, dialect, err
	default:
		// Unknown 方言 + deepseek-v4 模型：保持修复前行为，按原生 DeepSeek V4 规则处理。
		if isDeepSeekV4Model(mappedModel) {
			rewritten, err := NormalizeDeepSeekV4Thinking(body)
			return rewritten, dialect, err
		}
		return body, dialect, nil
	}
}

// resolveNormalizerName 将 ThinkingDialect 映射为诊断日志使用的 normalizer 名称。
func resolveNormalizerName(dialect ThinkingDialect) string {
	switch dialect {
	case ThinkingDialectSenseNova:
		return normalizerSenseNova
	case ThinkingDialectNativeDeepSeek:
		return normalizerDeepSeekV4
	default:
		return normalizerDeepSeekV4
	}
}

// resolveThinkingTransformRuleName 根据 incoming/outgoing thinking.type 和 dialect
// 推断转换规则名称，用于结构化日志。
func resolveThinkingTransformRuleName(incomingType, outgoingType string, dialect ThinkingDialect) string {
	if incomingType == outgoingType {
		return "no_transform"
	}
	switch dialect {
	case ThinkingDialectSenseNova:
		if incomingType == "adaptive" && outgoingType == "auto" {
			return "adaptive_to_auto"
		}
		return "type_changed:" + incomingType + "->" + outgoingType
	case ThinkingDialectNativeDeepSeek:
		if incomingType == "auto" && outgoingType == "adaptive" {
			return "auto_to_adaptive"
		}
		return "type_changed:" + incomingType + "->" + outgoingType
	default:
		return "type_changed:" + incomingType + "->" + outgoingType
	}
}

// logThinkingDialectTransform 记录 thinking 协议适配的结构化日志。
//
// 仅在 thinking.type 发生变化时输出（避免噪声），包含：
//   - request_id / account_id / mapped_model
//   - upstream_host（仅 hostname，不含 query / 凭据）
//   - thinking_dialect / incoming_type / outgoing_type / transform_rule
//
// 不打印完整请求体、messages、prompt、API Key、Token 或完整 Base URL。
// 当 GATEWAY_THINKING_DEBUG=true 时输出完整诊断日志（复用已有 provider_normalized 事件）。
func (s *GatewayService) logThinkingDialectTransform(
	ctx context.Context,
	account *Account,
	originalModel, mappedModel string,
	beforeType, afterType string,
	dialect ThinkingDialect,
	normalizerName string,
	stream bool,
) {
	// 始终输出一条精简的结构化日志（仅当 type 发生变化时）。
	if beforeType != afterType {
		reqID, _ := getRequestIDsFromContext(ctx)
		baseURL := resolveUpstreamBaseURL(account)
		upstreamHost := extractUpstreamHostFromBaseURL(baseURL)
		transformRule := resolveThinkingTransformRuleName(beforeType, afterType, dialect)

		logger.FromContext(ctx).Info("gateway.thinking.dialect_transform",
			zap.String("request_id", reqID),
			zap.Int64("account_id", accountIDOrZero(account)),
			zap.String("mapped_model", mappedModel),
			zap.String("upstream_host", upstreamHost),
			zap.String("thinking_dialect", string(dialect)),
			zap.String("incoming_type", beforeType),
			zap.String("outgoing_type", afterType),
			zap.String("transform_rule", transformRule),
		)
	}

	// 当 GATEWAY_THINKING_DEBUG=true 时，复用已有 provider_normalized 事件输出完整诊断。
	if s.debugThinkingEnabled() {
		s.logThinkingProviderNormalized(ctx, account, originalModel, mappedModel, beforeType, afterType, normalizerName, stream)
	}
}

// extractUpstreamHostFromBaseURL 从 Base URL 中提取 hostname（不含端口 / query / 凭据）。
func extractUpstreamHostFromBaseURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u == nil {
		return ""
	}
	return u.Hostname()
}
