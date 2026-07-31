package service

import (
	"fmt"
	"math"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Anthropic 协议顶层 thinking 字段的合法 type 枚举（入口侧）。
//
// 入口允许的值包括：
//   - enabled / disabled / auto：Anthropic 官方协议值。
//   - adaptive：本项目内部别名，用于 MiniMax 等第三方 Anthropic 兼容上游，
//     也可能是新版 Claude/Anthropic 模型的合法输入值。不在入口拒绝，
//     由下游 provider 适配层（如 DeepSeek V4）按需转换为上游契约值。
//
// 其他值（如 "high"、"on"、"true"）会在入口被拒绝，避免透传给任何上游。
const (
	anthropicThinkingTypeEnabled  = "enabled"
	anthropicThinkingTypeDisabled = "disabled"
	anthropicThinkingTypeAuto     = "auto"
	anthropicThinkingTypeAdaptive = "adaptive"
)

// MaxAnthropicThinkingBudgetTokens 是单请求 thinking.budget_tokens 的允许上限。
// 超过该值视为客户端误传，直接拒绝，避免透传后触发上游 400 或异常计费。
// 取值参考 Anthropic 文档对 extended thinking budget 的常见上限约束。
const MaxAnthropicThinkingBudgetTokens = 128000

// NormalizeAnthropicThinking 对 Anthropic 协议请求体中的顶层 thinking 字段做
// 统一校验与标准化。它是所有 Anthropic 路径（流式 / 非流式 / count_tokens /
// tool use / 多轮 / 重试）的单一入口，避免在多个 provider 或路由中重复实现。
//
// 规则：
//  1. thinking 字段不存在 → 返回 (原 body, false, nil)，不做任何改动。
//  2. thinking 为 null → 删除该字段，返回 (新 body, true, nil)。
//  3. thinking 不是 object（bool/number/string/array）→ 返回错误，调用方应返回 400。
//  4. thinking.type 缺失或非字符串 → 返回错误。
//  5. thinking.type 不在 {enabled, disabled, auto, adaptive} → 返回错误
//     （错误消息固定为 "thinking.type must be one of: enabled, disabled, auto, adaptive"）。
//     这会拒绝 "high"、"on"、"true" 等非法值，避免它们被透传给上游。
//     "adaptive" 是允许的输入值（MiniMax / 新版 Claude 可能使用），由下游
//     provider 适配层按需转换为上游契约值（如 DeepSeek V4 → auto）。
//  6. type=enabled|adaptive:
//     - budget_tokens 存在时必须为正整数（JSON number、整数、>0、非 NaN/Inf）；
//     字符串形式的数字、浮点数、0、负数均拒绝。
//     - budget_tokens 不超过 MaxAnthropicThinkingBudgetTokens。
//     - budget_tokens 缺失时不自动补齐（由下游按上游契约决定是否补充）。
//  7. type=disabled|auto: 移除 budget_tokens（若存在），因为上游仅 enabled 允许该字段。
//  8. 不会把缺失的 thinking 补成 {"type":null} 或 {}。
//  9. 不修改传入的 body 切片；如需改动返回新切片。
//
// 返回值：
//   - newBody: 标准化后的 body（可能与入参相同，也可能是新切片）
//   - hadThinking: 入参 body 中是否存在 thinking 字段（含 null）
//   - err: 校验失败时的错误（调用方应映射为 400 invalid_request_error）
func NormalizeAnthropicThinking(body []byte) (newBody []byte, hadThinking bool, err error) {
	if len(body) == 0 {
		return body, false, nil
	}

	res := gjson.GetBytes(body, "thinking")
	if !res.Exists() {
		return body, false, nil
	}
	hadThinking = true

	// {"thinking": null} → 删除字段，避免下游误判为空对象。
	if res.Type == gjson.Null {
		out, derr := sjson.DeleteBytes(body, "thinking")
		if derr != nil {
			return body, true, fmt.Errorf("thinking: failed to remove null field: %w", derr)
		}
		return out, true, nil
	}

	if !res.IsObject() {
		return body, true, fmt.Errorf("thinking: must be an object, got %s", jsonTypeName(res.Type))
	}

	typeRes := res.Get("type")
	if !typeRes.Exists() {
		return body, true, fmt.Errorf("thinking.type: field is required")
	}
	if typeRes.Type != gjson.String {
		return body, true, fmt.Errorf("thinking.type: must be a string, got %s", jsonTypeName(typeRes.Type))
	}

	t := strings.TrimSpace(typeRes.String())
	if !isValidAnthropicThinkingType(t) {
		return body, true, fmt.Errorf("thinking.type must be one of: enabled, disabled, auto, adaptive")
	}

	out := body
	changed := false

	switch t {
	case anthropicThinkingTypeEnabled, anthropicThinkingTypeAdaptive:
		if budgetErr := validateAnthropicThinkingBudget(res); budgetErr != nil {
			return body, true, budgetErr
		}
		// 不自动补齐 budget_tokens；保留客户端传入值。
		// adaptive 的 budget_tokens 由下游 provider 适配层决定是否移除。
	case anthropicThinkingTypeDisabled, anthropicThinkingTypeAuto:
		// disabled / auto 不允许携带 budget_tokens，移除避免上游 400。
		if res.Get("budget_tokens").Exists() {
			deleted, derr := sjson.DeleteBytes(out, "thinking.budget_tokens")
			if derr != nil {
				return body, true, fmt.Errorf("thinking.budget_tokens: failed to remove: %w", derr)
			}
			out = deleted
			changed = true
		}
	}

	// 规范化 type（去除首尾空格），保证下游读到的是干净枚举值。
	if t != typeRes.String() {
		set, serr := sjson.SetBytes(out, "thinking.type", t)
		if serr != nil {
			return body, true, fmt.Errorf("thinking.type: failed to normalize: %w", serr)
		}
		out = set
		changed = true
	}

	if !changed {
		return body, true, nil
	}
	return out, true, nil
}

// validateAnthropicThinkingBudget 校验 enabled 模式下的 budget_tokens。
// 仅当字段存在时校验；缺失时不报错（由调用方决定是否补充默认值）。
func validateAnthropicThinkingBudget(thinking gjson.Result) error {
	budget := thinking.Get("budget_tokens")
	if !budget.Exists() {
		return nil
	}
	if budget.Type != gjson.Number {
		return fmt.Errorf("thinking.budget_tokens: must be a positive integer, got %s", jsonTypeName(budget.Type))
	}
	f := budget.Float()
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("thinking.budget_tokens: must be a positive integer, got NaN or Inf")
	}
	if f != math.Trunc(f) {
		return fmt.Errorf("thinking.budget_tokens: must be an integer, got float %v", f)
	}
	if f <= 0 {
		return fmt.Errorf("thinking.budget_tokens: must be greater than 0, got %v", f)
	}
	if f > float64(MaxAnthropicThinkingBudgetTokens) {
		return fmt.Errorf("thinking.budget_tokens: must be <= %d, got %v", MaxAnthropicThinkingBudgetTokens, f)
	}
	return nil
}

func isValidAnthropicThinkingType(t string) bool {
	switch t {
	case anthropicThinkingTypeEnabled, anthropicThinkingTypeDisabled, anthropicThinkingTypeAuto, anthropicThinkingTypeAdaptive:
		return true
	}
	return false
}

// jsonTypeName 返回 gjson.Type 的可读名称，用于错误消息。
func jsonTypeName(t gjson.Type) string {
	switch t {
	case gjson.Null:
		return "null"
	case gjson.False:
		return "boolean"
	case gjson.Number:
		return "number"
	case gjson.String:
		return "string"
	case gjson.True:
		return "boolean"
	default:
		return "unknown"
	}
}
