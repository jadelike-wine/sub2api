// Package service 提供 Agnes 上游请求规范化器（AgnesRequestNormalizer）。
//
// 职责：
//   - 根据服务端配置决定 chat_template_kwargs.enable_thinking 最终值
//   - 阻止客户端通过 chat_template_kwargs.enable_thinking / include_reasoning /
//     return_reasoning / expose_reasoning / 顶层 thinking / reasoning_effort 等
//     字段绕过服务端策略
//   - 不向 Agnes 注入其文档未确认支持的 reasoning_effort 字段
//   - 不原样透传 Anthropic 格式的顶层 thinking 对象
//
// 与 Prompt Guard 的边界：Prompt Guard 负责扫描用户输入中的 jailbreak；
// 本规范化器只负责上游请求参数规范化，不做语义判定。
// 与 ResponseSanitizer 的边界：本规范化器只规范化请求；响应层清洗（删除
// reasoning_content 及别名、重写公开 model）由 gateway_identity_prompt.go
// 中的 redactChatCompletionsResponse / redactChatCompletionsStreamChunk 在
// stripReasoning=true 时执行。Agnes 适配账号默认开启剥离；DeepSeek 等
// 显式暴露 reasoning 给用户的模型不开启，以保留其合法 reasoning_content。
package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AgnesThinkingMode 常量：disabled / enabled / auto。
const (
	AgnesThinkingModeDisabled = "disabled"
	AgnesThinkingModeEnabled  = "enabled"
	AgnesThinkingModeAuto     = "auto"
)

// agnesClientBypassFields 是客户端可能用来绕过服务端 Thinking 策略或
// 索取 reasoning 的字段。无论是否启用 Agnes 适配，只要走 Agnes 上游
// 都必须剥离，避免客户端通过自定义字段改变 expose_reasoning。
//
// 注意：reasoning_effort 是 OpenAI 合法字段，但 Agnes 文档未确认支持，
// 剥离后由服务端决定是否启用 thinking（通过 chat_template_kwargs）。
var agnesClientBypassFields = []string{
	"include_reasoning",
	"return_reasoning",
	"expose_reasoning",
	"reasoning_effort",
}

// AgnesThinkingNormalizationResult 描述一次 Agnes 请求规范化的结果，
// 供日志/指标使用。不携带 reasoning 原文或上游模型名。
type AgnesThinkingNormalizationResult struct {
	// Applied 表示是否对 body 做了规范化（true 时 body 已被改写）。
	Applied bool
	// Mode 使用的 Thinking 模式（disabled/enabled/auto）。
	Mode string
	// EffectiveEnableThinking 最终发往 Agnes 的 enable_thinking 值。
	EffectiveEnableThinking bool
	// ClientHadEnableThinking 客户端是否传入了 chat_template_kwargs.enable_thinking。
	ClientHadEnableThinking bool
	// ClientEnableThinkingValue 客户端传入的 enable_thinking 原始布尔值（仅当类型为 bool 时有效）。
	ClientEnableThinkingValue bool
	// StrippedBypassFields 被剥离的绕过字段名列表（不含值）。
	StrippedBypassFields []string
	// StrippedAnthropicThinking 是否剥离了顶层 thinking 对象（Anthropic 格式）。
	StrippedAnthropicThinking bool
	// AutoTriggeredSignals auto 模式下命中启用 thinking 的信号列表。
	AutoTriggeredSignals []string
}

// normalizeAgnesThinkingRequest 对发往 Agnes 上游的 OpenAI 兼容 Chat Completions
// 请求做服务端策略规范化。
//
// 行为：
//  1. 剥离客户端绕过字段（include_reasoning / return_reasoning / expose_reasoning / reasoning_effort）
//  2. 剥离顶层 thinking 对象（Anthropic 格式，Agnes 不支持）
//  3. 解析客户端 chat_template_kwargs.enable_thinking（容错：null/字符串/数组/异常对象均不 panic）
//  4. 按 cfg.Mode 决定最终 enable_thinking：
//     - disabled: 强制 false
//     - enabled:  强制 true
//     - auto:     按确定性规则决定
//  5. 写入 chat_template_kwargs.enable_thinking=<最终值>（覆盖客户端值）
//  6. 不修改 model 字段（由调用方在更早阶段通过 ResolvedModel 处理）
//
// 返回规范化后的 body 与结果描述。body 解析失败时原样返回，Applied=false。
func normalizeAgnesThinkingRequest(body []byte, cfg config.AgnesThinkingConfig) ([]byte, AgnesThinkingNormalizationResult) {
	res := AgnesThinkingNormalizationResult{Mode: normalizeAgnesThinkingMode(cfg.Mode)}
	if len(body) == 0 {
		return body, res
	}

	out := body

	// 1. 剥离客户端绕过字段
	for _, field := range agnesClientBypassFields {
		if gjson.GetBytes(out, field).Exists() {
			if deleted, err := sjson.DeleteBytes(out, field); err == nil {
				out = deleted
				res.StrippedBypassFields = append(res.StrippedBypassFields, field)
				res.Applied = true
			}
		}
	}

	// 2. 剥离顶层 thinking 对象（Anthropic 格式：{"thinking":{"type":"enabled",...}}）
	//    Agnes OpenAI 兼容接口不应原样透传 Anthropic 格式。
	if gjson.GetBytes(out, "thinking").Exists() {
		if deleted, err := sjson.DeleteBytes(out, "thinking"); err == nil {
			out = deleted
			res.StrippedAnthropicThinking = true
			res.Applied = true
		}
	}

	// 3. 解析客户端 chat_template_kwargs.enable_thinking（容错）
	clientEnableThinking, clientHadEnableThinking := parseAgnesClientEnableThinking(out)
	res.ClientHadEnableThinking = clientHadEnableThinking
	res.ClientEnableThinkingValue = clientEnableThinking

	// 4. 决定最终 enable_thinking
	effective, signals := resolveAgnesEffectiveThinking(out, cfg)
	res.EffectiveEnableThinking = effective
	if len(signals) > 0 {
		res.AutoTriggeredSignals = signals
	}

	// 5. 写入 chat_template_kwargs.enable_thinking=<最终值>
	//    无论客户端是否传入，最终值都由服务端决定。
	//    使用 sjson 路径语法，会自动创建中间对象。
	updated, err := sjson.SetBytes(out, "chat_template_kwargs.enable_thinking", effective)
	if err != nil {
		// sjson 失败时保留剥离后的 body（仍 Applied=true），但不写入最终值。
		// 这种情况极罕见（仅当 chat_template_kwargs 是非对象类型时）。
		// 兜底：先删除 chat_template_kwargs 再重建为对象。
		if cleared, derr := sjson.DeleteBytes(out, "chat_template_kwargs"); derr == nil {
			out = cleared
		}
		updated, err = sjson.SetBytes(out, "chat_template_kwargs.enable_thinking", effective)
		if err != nil {
			return out, res
		}
	}
	out = updated
	res.Applied = true

	return out, res
}

// normalizeAgnesThinkingMode 归一化 Mode 字符串，非法值回退为 auto。
func normalizeAgnesThinkingMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AgnesThinkingModeDisabled:
		return AgnesThinkingModeDisabled
	case AgnesThinkingModeEnabled:
		return AgnesThinkingModeEnabled
	case AgnesThinkingModeAuto:
		return AgnesThinkingModeAuto
	default:
		return AgnesThinkingModeAuto
	}
}

// parseAgnesClientEnableThinking 容错解析客户端传入的
// chat_template_kwargs.enable_thinking 字段。
//
// 仅当值为 JSON bool 时返回 (value, true)；其他类型（null/字符串/数组/对象）
// 返回 (false, true) 表示字段存在但值非法——服务端仍会覆盖。
// 字段不存在时返回 (false, false)。
func parseAgnesClientEnableThinking(body []byte) (bool, bool) {
	result := gjson.GetBytes(body, "chat_template_kwargs.enable_thinking")
	if !result.Exists() {
		return false, false
	}
	if result.Type != gjson.True && result.Type != gjson.False {
		// 非布尔类型：字段存在但值非法，视为客户端尝试绕过
		return false, true
	}
	return result.Bool(), true
}

// resolveAgnesEffectiveThinking 按配置模式决定最终 enable_thinking 值。
//
// disabled: false
// enabled:  true
// auto:     按确定性规则（信号）决定：
//   - 包含 tools 数组且非空 → true
//   - 指定 tool_choice → true
//   - 输入字符数超过阈值 → true
//   - 内容包含 ``` 代码块 → true
//   - messages 数量超过多轮阈值 → true
//   - 否则 false
//
// 返回 (effective, signals)。signals 仅在 auto 模式下非空，用于日志/指标。
func resolveAgnesEffectiveThinking(body []byte, cfg config.AgnesThinkingConfig) (bool, []string) {
	mode := normalizeAgnesThinkingMode(cfg.Mode)
	switch mode {
	case AgnesThinkingModeDisabled:
		return false, nil
	case AgnesThinkingModeEnabled:
		return true, nil
	default: // auto
		return resolveAgnesAutoThinking(body, cfg)
	}
}

// resolveAgnesAutoThinking 实现 auto 模式的确定性规则。
// 不调用任何外部模型，仅基于请求结构判断。
func resolveAgnesAutoThinking(body []byte, cfg config.AgnesThinkingConfig) (bool, []string) {
	var signals []string

	// 信号 1: tools 数组非空
	if cfg.AutoEnableOnTools {
		toolsResult := gjson.GetBytes(body, "tools")
		if toolsResult.IsArray() && len(toolsResult.Array()) > 0 {
			signals = append(signals, "tools")
		}
	}

	// 信号 2: tool_choice 已指定（非空字符串/对象）
	toolChoice := gjson.GetBytes(body, "tool_choice")
	if toolChoice.Exists() {
		// tool_choice="none" 或 {"type":"none"} 不算启用
		if isAgnesToolChoiceActive(toolChoice) {
			signals = append(signals, "tool_choice")
		}
	}

	// 信号 3: 输入字符数超过阈值
	threshold := cfg.AutoInputCharsThreshold
	if threshold <= 0 {
		threshold = 2000
	}
	totalChars := agnesRequestInputCharCount(body)
	if totalChars >= threshold {
		signals = append(signals, "long_input")
	}

	// 信号 4: 内容包含 ``` 代码块
	if cfg.AutoEnableOnCodeBlock {
		if agnesRequestHasCodeBlock(body) {
			signals = append(signals, "code_block")
		}
	}

	// 信号 5: 多轮对话
	multiTurnThreshold := cfg.AutoEnableOnMultiTurnThreshold
	if multiTurnThreshold <= 0 {
		multiTurnThreshold = 2
	}
	if agnesRequestMessageCount(body) > multiTurnThreshold {
		signals = append(signals, "multi_turn")
	}

	return len(signals) > 0, signals
}

// isAgnesToolChoiceActive 判断 tool_choice 是否表示主动调用工具。
// "none" / {"type":"none"} / "auto" 不视为主动调用；其他值视为主动调用。
func isAgnesToolChoiceActive(result gjson.Result) bool {
	if result.Type == gjson.String {
		v := strings.ToLower(strings.TrimSpace(result.String()))
		return v != "" && v != "none" && v != "auto"
	}
	if result.IsObject() {
		t := strings.ToLower(strings.TrimSpace(result.Get("type").String()))
		return t != "" && t != "none" && t != "auto"
	}
	return false
}

// agnesRequestInputCharCount 累计 messages 中所有文本内容的字符数。
// 仅统计字符串 content 与 content[].text，不解码 image_url 等二进制内容。
func agnesRequestInputCharCount(body []byte) int {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return 0
	}
	total := 0
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.Exists() {
			continue
		}
		if content.Type == gjson.String {
			total += len(content.String())
			continue
		}
		if content.IsArray() {
			for _, part := range content.Array() {
				if part.Get("type").String() == "text" {
					total += len(part.Get("text").String())
				}
			}
		}
	}
	return total
}

// agnesRequestHasCodeBlock 检查 messages 内容中是否包含 ``` 代码块标记。
func agnesRequestHasCodeBlock(body []byte) bool {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.Exists() {
			continue
		}
		if content.Type == gjson.String {
			if strings.Contains(content.String(), "```") {
				return true
			}
			continue
		}
		if content.IsArray() {
			for _, part := range content.Array() {
				if part.Get("type").String() == "text" {
					if strings.Contains(part.Get("text").String(), "```") {
						return true
					}
				}
			}
		}
	}
	return false
}

// agnesRequestMessageCount 返回 messages 数组的长度。
func agnesRequestMessageCount(body []byte) int {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return 0
	}
	return len(messages.Array())
}
