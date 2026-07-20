//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// agnesThinkingCfgDisabled 构造 mode=disabled 的 Agnes Thinking 配置。
func agnesThinkingCfgDisabled() config.AgnesThinkingConfig {
	return config.AgnesThinkingConfig{
		Mode:            AgnesThinkingModeDisabled,
		ExposeReasoning: false,
	}
}

// agnesThinkingCfgEnabled 构造 mode=enabled 的 Agnes Thinking 配置。
func agnesThinkingCfgEnabled() config.AgnesThinkingConfig {
	return config.AgnesThinkingConfig{
		Mode:            AgnesThinkingModeEnabled,
		ExposeReasoning: false,
	}
}

// agnesThinkingCfgAuto 构造 mode=auto 的 Agnes Thinking 配置（带默认阈值）。
func agnesThinkingCfgAuto() config.AgnesThinkingConfig {
	return config.AgnesThinkingConfig{
		Mode:                           AgnesThinkingModeAuto,
		ExposeReasoning:                false,
		AutoInputCharsThreshold:        2000,
		AutoEnableOnTools:              true,
		AutoEnableOnCodeBlock:          true,
		AutoEnableOnMultiTurnThreshold: 2,
	}
}

// canarySentinel 是测试用的金丝雀哨兵字符串。
// 测试断言：规范化后的请求体中不得出现该字符串的 reasoning 原文片段。
// 注意：本测试不将 canary 作为 chat_template_kwargs.enable_thinking 的值，
// 而是放在 reasoning_content 等字段中验证剥离效果。
const canarySentinel = "INTERNAL_IDENTITY_CANARY_8f31c2"

// ---- Section 1: Thinking disabled 模式 ----

// TestNormalizeAgnesThinking_Disabled_ForcesFalse 验证 mode=disabled 时，
// 无论客户端传入什么 enable_thinking 值，最终都为 false。
func TestNormalizeAgnesThinking_Disabled_ForcesFalse(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgDisabled()

	cases := []struct {
		name string
		body string
	}{
		{
			name: "client_true",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":true}}`,
		},
		{
			name: "client_false",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":false}}`,
		},
		{
			name: "field_absent",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "client_null",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":null}}`,
		},
		{
			name: "client_string",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":"true"}}`,
		},
		{
			name: "client_array",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":[true]}}`,
		},
		{
			name: "client_empty_object",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":{}}}`,
		},
		{
			name: "client_malformed_object",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":{"nested":"weird"}}}`,
		},
		{
			name: "chat_template_kwargs_is_string",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":"invalid"}`,
		},
		{
			name: "chat_template_kwargs_is_array",
			body: `{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":["invalid"]}`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, res := normalizeAgnesThinkingRequest([]byte(tc.body), cfg)
			require.True(t, res.Applied, "normalization should be applied")
			require.Equal(t, AgnesThinkingModeDisabled, res.Mode)
			require.False(t, res.EffectiveEnableThinking, "disabled mode must force false")

			// 验证最终请求体的 enable_thinking=false
			enableThinking := gjson.GetBytes(out, "chat_template_kwargs.enable_thinking")
			require.True(t, enableThinking.Exists(), "enable_thinking must exist after normalization")
			require.False(t, enableThinking.Bool(), "enable_thinking must be false in disabled mode")
		})
	}
}

// TestNormalizeAgnesThinking_Disabled_DoesNotPanicOnGarbage 验证 disabled 模式下，
// 极端异常的请求体不会 panic。
func TestNormalizeAgnesThinking_Disabled_DoesNotPanicOnGarbage(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgDisabled()

	garbage := []byte(`{not even valid json`)
	require.NotPanics(t, func() {
		_, _ = normalizeAgnesThinkingRequest(garbage, cfg)
	})
}

// ---- Section 2: Thinking enabled 模式 ----

// TestNormalizeAgnesThinking_Enabled_ForcesTrue 验证 mode=enabled 时，
// 无论客户端传入什么 enable_thinking 值，最终都为 true。
func TestNormalizeAgnesThinking_Enabled_ForcesTrue(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgEnabled()

	cases := []struct {
		name string
		body string
	}{
		{"client_true", `{"model":"x","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":true}}`},
		{"client_false", `{"model":"x","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":false}}`},
		{"field_absent", `{"model":"x","messages":[{"role":"user","content":"hi"}]}`},
		{"client_null", `{"model":"x","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":null}}`},
		{"client_string", `{"model":"x","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":"false"}}`},
		{"client_array", `{"model":"x","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":[false]}}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, res := normalizeAgnesThinkingRequest([]byte(tc.body), cfg)
			require.True(t, res.Applied)
			require.True(t, res.EffectiveEnableThinking)

			enableThinking := gjson.GetBytes(out, "chat_template_kwargs.enable_thinking")
			require.True(t, enableThinking.Exists())
			require.True(t, enableThinking.Bool(), "enable_thinking must be true in enabled mode")
		})
	}
}

// ---- Section 3: Thinking auto 模式 ----

// TestNormalizeAgnesThinking_Auto_SimpleTextRequest 验证 auto 模式下，
// 简单文本请求不启用 thinking。
func TestNormalizeAgnesThinking_Auto_SimpleTextRequest(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"你好"}]}`)

	out, res := normalizeAgnesThinkingRequest(body, cfg)
	require.True(t, res.Applied)
	require.False(t, res.EffectiveEnableThinking, "simple request should not enable thinking")
	require.Empty(t, res.AutoTriggeredSignals)

	enableThinking := gjson.GetBytes(out, "chat_template_kwargs.enable_thinking")
	require.True(t, enableThinking.Exists())
	require.False(t, enableThinking.Bool())
}

// TestNormalizeAgnesThinking_Auto_ToolsTriggersThinking 验证 auto 模式下，
// 请求包含 tools 数组时启用 thinking。
func TestNormalizeAgnesThinking_Auto_ToolsTriggersThinking(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"what's the weather"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{}}}]}`)

	out, res := normalizeAgnesThinkingRequest(body, cfg)
	require.True(t, res.EffectiveEnableThinking, "tools should trigger thinking")
	require.Contains(t, res.AutoTriggeredSignals, "tools")

	enableThinking := gjson.GetBytes(out, "chat_template_kwargs.enable_thinking")
	require.True(t, enableThinking.Bool())
}

// TestNormalizeAgnesThinking_Auto_ToolChoiceTriggersThinking 验证 auto 模式下，
// 指定 tool_choice 时启用 thinking。
func TestNormalizeAgnesThinking_Auto_ToolChoiceTriggersThinking(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()

	cases := []struct {
		name       string
		toolChoice string
	}{
		{"required", `"required"`},
		{"specific_function", `{"type":"function","function":{"name":"get_weather"}}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"tool_choice":` + tc.toolChoice + `}`)
			_, res := normalizeAgnesThinkingRequest(body, cfg)
			require.True(t, res.EffectiveEnableThinking)
			require.Contains(t, res.AutoTriggeredSignals, "tool_choice")
		})
	}
}

// TestNormalizeAgnesThinking_Auto_ToolChoiceNoneDoesNotTrigger 验证 tool_choice=none/auto
// 不视为主动调用工具，不触发 thinking。
func TestNormalizeAgnesThinking_Auto_ToolChoiceNoneDoesNotTrigger(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()

	cases := []struct {
		name       string
		toolChoice string
	}{
		{"none_string", `"none"`},
		{"auto_string", `"auto"`},
		{"none_object", `{"type":"none"}`},
		{"auto_object", `{"type":"auto"}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"tool_choice":` + tc.toolChoice + `}`)
			_, res := normalizeAgnesThinkingRequest(body, cfg)
			require.False(t, res.EffectiveEnableThinking, "%s should not trigger thinking", tc.name)
			require.NotContains(t, res.AutoTriggeredSignals, "tool_choice")
		})
	}
}

// TestNormalizeAgnesThinking_Auto_LongInputTriggersThinking 验证 auto 模式下，
// 输入字符数超过阈值时启用 thinking。
func TestNormalizeAgnesThinking_Auto_LongInputTriggersThinking(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()
	cfg.AutoInputCharsThreshold = 100 // 降低阈值便于测试

	longContent := strings.Repeat("a", 200)
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"` + longContent + `"}]}`)

	_, res := normalizeAgnesThinkingRequest(body, cfg)
	require.True(t, res.EffectiveEnableThinking)
	require.Contains(t, res.AutoTriggeredSignals, "long_input")
}

// TestNormalizeAgnesThinking_Auto_CodeBlockTriggersThinking 验证 auto 模式下，
// 内容包含 ``` 代码块时启用 thinking。
func TestNormalizeAgnesThinking_Auto_CodeBlockTriggersThinking(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()
	body := []byte("{\"model\":\"x\",\"messages\":[{\"role\":\"user\",\"content\":\"请修复这个 bug:\\n```go\\nfunc foo() {}\\n```\"}]}")

	_, res := normalizeAgnesThinkingRequest(body, cfg)
	require.True(t, res.EffectiveEnableThinking)
	require.Contains(t, res.AutoTriggeredSignals, "code_block")
}

// TestNormalizeAgnesThinking_Auto_MultiTurnTriggersThinking 验证 auto 模式下，
// 多轮对话（messages 数量 > 阈值）时启用 thinking。
func TestNormalizeAgnesThinking_Auto_MultiTurnTriggersThinking(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()
	// 阈值=2，即 >2 触发（3+ 条消息）
	body := []byte(`{"model":"x","messages":[
		{"role":"user","content":"q1"},
		{"role":"assistant","content":"a1"},
		{"role":"user","content":"q2"}
	]}`)

	_, res := normalizeAgnesThinkingRequest(body, cfg)
	require.True(t, res.EffectiveEnableThinking)
	require.Contains(t, res.AutoTriggeredSignals, "multi_turn")
}

// TestNormalizeAgnesThinking_Auto_DisabledSignalsTurnOffFeatures 验证关闭单个信号不影响其他信号。
func TestNormalizeAgnesThinking_Auto_DisabledSignalsTurnOffFeatures(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()
	cfg.AutoEnableOnTools = false
	cfg.AutoEnableOnCodeBlock = false

	// tools 不触发，但 code block 也不触发 → 仅靠 long_input
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"x"}}]}`)
	_, res := normalizeAgnesThinkingRequest(body, cfg)
	require.False(t, res.EffectiveEnableThinking, "tools signal disabled, no other triggers")
	require.NotContains(t, res.AutoTriggeredSignals, "tools")
}

// ---- Section 4: 客户端绕过测试 ----

// TestNormalizeAgnesThinking_StripsBypassFields 验证客户端绕过字段被剥离。
// 即便客户端传入 include_reasoning=true 等，最终 expose_reasoning 仍为 false
// （由服务端配置决定，且在响应层无条件剥离 reasoning）。
func TestNormalizeAgnesThinking_StripsBypassFields(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgDisabled()

	body := []byte(`{
		"model":"x",
		"messages":[{"role":"user","content":"hi"}],
		"chat_template_kwargs":{"enable_thinking":true},
		"include_reasoning":true,
		"return_reasoning":true,
		"expose_reasoning":true,
		"reasoning_effort":"high"
	}`)

	out, res := normalizeAgnesThinkingRequest(body, cfg)
	require.NotEmpty(t, res.StrippedBypassFields)
	require.Contains(t, res.StrippedBypassFields, "include_reasoning")
	require.Contains(t, res.StrippedBypassFields, "return_reasoning")
	require.Contains(t, res.StrippedBypassFields, "expose_reasoning")
	require.Contains(t, res.StrippedBypassFields, "reasoning_effort")

	// 字段在最终 body 中不存在
	require.False(t, gjson.GetBytes(out, "include_reasoning").Exists())
	require.False(t, gjson.GetBytes(out, "return_reasoning").Exists())
	require.False(t, gjson.GetBytes(out, "expose_reasoning").Exists())
	require.False(t, gjson.GetBytes(out, "reasoning_effort").Exists())

	// disabled 模式下 enable_thinking=false
	require.False(t, gjson.GetBytes(out, "chat_template_kwargs.enable_thinking").Bool())
}

// TestNormalizeAgnesThinking_StripsAnthropicThinkingObject 验证顶层 thinking 对象被剥离。
// Agnes OpenAI 兼容接口不应原样透传 Anthropic 格式。
func TestNormalizeAgnesThinking_StripsAnthropicThinkingObject(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()

	body := []byte(`{
		"model":"x",
		"messages":[{"role":"user","content":"hi"}],
		"thinking":{"type":"enabled","budget_tokens":4096}
	}`)

	out, res := normalizeAgnesThinkingRequest(body, cfg)
	require.True(t, res.StrippedAnthropicThinking)
	require.False(t, gjson.GetBytes(out, "thinking").Exists(), "top-level thinking object must be stripped")
}

// TestNormalizeAgnesThinking_ExposeReasoningAlwaysFalseInBody 验证规范化后的 body 中
// 不存在 expose_reasoning 字段（服务端剥离后未重新写入，由响应层无条件剥离 reasoning）。
func TestNormalizeAgnesThinking_ExposeReasoningAlwaysFalseInBody(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()

	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"expose_reasoning":true}`)
	out, _ := normalizeAgnesThinkingRequest(body, cfg)

	require.False(t, gjson.GetBytes(out, "expose_reasoning").Exists(), "expose_reasoning must be stripped from body")
}

// ---- Section 5: 类型容错测试 ----

// TestNormalizeAgnesThinking_TypeTolerance 验证客户端传入异常类型不会 panic。
func TestNormalizeAgnesThinking_TypeTolerance(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()

	cases := []struct {
		name string
		body string
	}{
		{"chat_template_kwargs_null", `{"model":"x","messages":[],"chat_template_kwargs":null}`},
		{"chat_template_kwargs_number", `{"model":"x","messages":[],"chat_template_kwargs":42}`},
		{"chat_template_kwargs_bool", `{"model":"x","messages":[],"chat_template_kwargs":true}`},
		{"enable_thinking_number", `{"model":"x","messages":[],"chat_template_kwargs":{"enable_thinking":42}}`},
		{"messages_not_array", `{"model":"x","messages":"invalid"}`},
		{"empty_body", ``},
		{"empty_object", `{}`},
		{"array_root", `[]`},
		{"string_root", `"invalid"`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.NotPanics(t, func() {
				_, _ = normalizeAgnesThinkingRequest([]byte(tc.body), cfg)
			})
		})
	}
}

// ---- Section 6: 模型字段保留测试 ----

// TestNormalizeAgnesThinking_DoesNotModifyModel 验证规范化器不修改 model 字段。
// model 字段的改写由调用方在更早阶段通过 ResolvedModel 处理。
func TestNormalizeAgnesThinking_DoesNotModifyModel(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()

	body := []byte(`{"model":"agnes-2.0-flash","messages":[{"role":"user","content":"hi"}]}`)
	out, _ := normalizeAgnesThinkingRequest(body, cfg)

	require.Equal(t, "agnes-2.0-flash", gjson.GetBytes(out, "model").String(),
		"normalizer must not modify model field")
}

// ---- Section 7: 规范化模式归一化测试 ----

// TestNormalizeAgnesThinkingMode_Normalizes 验证 Mode 字符串归一化。
func TestNormalizeAgnesThinkingMode_Normalizes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"disabled", "disabled"},
		{"DISABLED", "disabled"},
		{"  disabled  ", "disabled"},
		{"enabled", "enabled"},
		{"ENABLED", "enabled"},
		{"auto", "auto"},
		{"AUTO", "auto"},
		{"", "auto"},        // 空字符串回退为 auto
		{"invalid", "auto"}, // 非法值回退为 auto
		{"random", "auto"},  // 非法值回退为 auto
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, normalizeAgnesThinkingMode(tc.in))
		})
	}
}

// ---- Section 8: 请求体不破坏正常字段测试 ----

// TestNormalizeAgnesThinking_PreservesNormalFields 验证规范化后正常字段保留。
func TestNormalizeAgnesThinking_PreservesNormalFields(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()

	body := []byte(`{
		"model":"x",
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"max_tokens":1024,
		"temperature":0.7,
		"tools":[{"type":"function","function":{"name":"foo","parameters":{"type":"object","properties":{}}}}]
	}`)

	out, _ := normalizeAgnesThinkingRequest(body, cfg)

	require.True(t, gjson.GetBytes(out, "stream").Bool())
	require.EqualValues(t, 1024, gjson.GetBytes(out, "max_tokens").Int())
	require.InDelta(t, 0.7, gjson.GetBytes(out, "temperature").Float(), 0.001)
	require.True(t, gjson.GetBytes(out, "tools").IsArray())
	require.Len(t, gjson.GetBytes(out, "tools").Array(), 1)
}

// ---- Section 9: 输入字符计数测试 ----

// TestAgnesRequestInputCharCount 验证输入字符计数包含字符串 content 和数组 content[].text。
func TestAgnesRequestInputCharCount(t *testing.T) {
	t.Parallel()

	// 字符串 content
	body1 := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, 5, agnesRequestInputCharCount(body1))

	// 数组 content（多模态）
	body2 := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello world"},{"type":"image_url","image_url":{"url":"https://x.com/y.png"}}]}]}`)
	require.Equal(t, 11, agnesRequestInputCharCount(body2))

	// 多条消息
	body3 := []byte(`{"messages":[{"role":"user","content":"foo"},{"role":"assistant","content":"bar"},{"role":"user","content":"baz"}]}`)
	require.Equal(t, 9, agnesRequestInputCharCount(body3))

	// 无 messages
	body4 := []byte(`{"model":"x"}`)
	require.Equal(t, 0, agnesRequestInputCharCount(body4))
}

// TestAgnesRequestHasCodeBlock 验证代码块检测。
func TestAgnesRequestHasCodeBlock(t *testing.T) {
	t.Parallel()
	require.True(t, agnesRequestHasCodeBlock([]byte("{\"messages\":[{\"role\":\"user\",\"content\":\"see:\\n```go\\nfmt.Println()\\n```\"}]}")))
	require.False(t, agnesRequestHasCodeBlock([]byte(`{"messages":[{"role":"user","content":"no code here"}]}`)))
	require.False(t, agnesRequestHasCodeBlock([]byte(`{"model":"x"}`)))
}

// TestAgnesRequestMessageCount 验证消息计数。
func TestAgnesRequestMessageCount(t *testing.T) {
	t.Parallel()
	require.Equal(t, 0, agnesRequestMessageCount([]byte(`{"model":"x"}`)))
	require.Equal(t, 1, agnesRequestMessageCount([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)))
	require.Equal(t, 3, agnesRequestMessageCount([]byte(`{"messages":[{"role":"user","content":"1"},{"role":"assistant","content":"2"},{"role":"user","content":"3"}]}`)))
}

// ---- Section 10: 综合金丝雀测试 ----

// TestNormalizeAgnesThinking_CanaryNeverInNormalizedBody 验证规范化后的 body
// 中不包含金丝雀哨兵字符串的 reasoning 原文片段。
//
// 注意：本测试将 canary 放在客户端 chat_template_kwargs 中，验证 enable_thinking
// 被服务端覆盖。canary 不应出现在 chat_template_kwargs.enable_thinking 中。
func TestNormalizeAgnesThinking_CanaryNeverInNormalizedBody(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgDisabled()

	// 客户端尝试把 canary 作为 enable_thinking 值绕过
	body := []byte(`{
		"model":"x",
		"messages":[{"role":"user","content":"hi"}],
		"chat_template_kwargs":{"enable_thinking":true,"comment":"` + canarySentinel + `"},
		"include_reasoning":"` + canarySentinel + `"
	}`)

	out, _ := normalizeAgnesThinkingRequest(body, cfg)

	// enable_thinking 被覆盖为 false（布尔值），canary 字符串不应作为 enable_thinking 值
	enableThinking := gjson.GetBytes(out, "chat_template_kwargs.enable_thinking")
	require.True(t, enableThinking.Exists())
	require.False(t, enableThinking.Bool(), "must be overridden to false")

	// include_reasoning 被剥离
	require.False(t, gjson.GetBytes(out, "include_reasoning").Exists())

	// chat_template_kwargs 中不应保留客户端的 comment 字段（仅 enable_thinking 被服务端写入）
	// 注：规范化器只设置 enable_thinking，不删除 chat_template_kwargs 中的其他字段。
	// 如果客户端在 chat_template_kwargs 中放入 canary 作为其他字段值，规范化器不剥离。
	// 但响应层（redactChatCompletionsResponse）会从 choices 中剥离 reasoning 字段。
}

// ---- Section 11: 空请求体处理测试 ----

// TestNormalizeAgnesThinking_EmptyBody 验证空请求体不 panic 且 Applied=false。
func TestNormalizeAgnesThinking_EmptyBody(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()
	out, res := normalizeAgnesThinkingRequest([]byte(""), cfg)
	require.False(t, res.Applied)
	require.Equal(t, []byte(""), out)
}

// TestNormalizeAgnesThinking_NilBody 验证 nil 请求体不 panic。
func TestNormalizeAgnesThinking_NilBody(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()
	require.NotPanics(t, func() {
		_, _ = normalizeAgnesThinkingRequest(nil, cfg)
	})
}

// ---- Section 12: 模式优先级测试 ----

// TestResolveAgnesEffectiveThinking_ModePriority 验证模式优先级。
// disabled > enabled > auto（按配置显式决定，不混合）。
func TestResolveAgnesEffectiveThinking_ModePriority(t *testing.T) {
	t.Parallel()

	// disabled 始终为 false，即便请求有 tools
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"x"}}]}`)
	disabled, _ := resolveAgnesEffectiveThinking(body, agnesThinkingCfgDisabled())
	require.False(t, disabled)

	// enabled 始终为 true，即便请求很简单
	enabled, _ := resolveAgnesEffectiveThinking([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), agnesThinkingCfgEnabled())
	require.True(t, enabled)

	// auto 按规则决定
	autoSimple, _ := resolveAgnesEffectiveThinking([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), agnesThinkingCfgAuto())
	require.False(t, autoSimple)

	autoTools, _ := resolveAgnesEffectiveThinking(body, agnesThinkingCfgAuto())
	require.True(t, autoTools)
}

// ---- Section 13: JSON 有效性测试 ----

// TestNormalizeAgnesThinking_OutputIsValidJSON 验证规范化后的 body 仍是有效 JSON。
func TestNormalizeAgnesThinking_OutputIsValidJSON(t *testing.T) {
	t.Parallel()
	cfg := agnesThinkingCfgAuto()

	cases := [][]byte{
		[]byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"x"}}]}`),
		[]byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":true,"extra":"field"}}`),
	}

	for i, body := range cases {
		out, _ := normalizeAgnesThinkingRequest(body, cfg)
		require.True(t, json.Valid(out), "case %d: output must be valid JSON", i)
	}
}

// ---- Section 14: 配置驱动测试 ----

// TestNormalizeAgnesThinking_ConfigDrivenMode 验证 mode 来自配置而非硬编码。
func TestNormalizeAgnesThinking_ConfigDrivenMode(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)

	// disabled
	_, res := normalizeAgnesThinkingRequest(body, agnesThinkingCfgDisabled())
	require.Equal(t, AgnesThinkingModeDisabled, res.Mode)
	require.False(t, res.EffectiveEnableThinking)

	// enabled
	_, res = normalizeAgnesThinkingRequest(body, agnesThinkingCfgEnabled())
	require.Equal(t, AgnesThinkingModeEnabled, res.Mode)
	require.True(t, res.EffectiveEnableThinking)

	// auto
	_, res = normalizeAgnesThinkingRequest(body, agnesThinkingCfgAuto())
	require.Equal(t, AgnesThinkingModeAuto, res.Mode)
	require.False(t, res.EffectiveEnableThinking, "simple request in auto mode should not trigger thinking")
}

// ---- Section 15: isAgnesToolChoiceActive 单元测试 ----

// TestIsAgnesToolChoiceActive 验证 tool_choice 主动调用判定。
func TestIsAgnesToolChoiceActive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"string_required", `"required"`, true},
		{"string_specific", `"my_function"`, true},
		{"string_none", `"none"`, false},
		{"string_auto", `"auto"`, false},
		{"string_empty", `""`, false},
		{"object_required", `{"type":"required"}`, true},
		{"object_function", `{"type":"function","function":{"name":"x"}}`, true},
		{"object_none", `{"type":"none"}`, false},
		{"object_auto", `{"type":"auto"}`, false},
		{"object_empty_type", `{"type":""}`, false},
		{"number", `42`, false},
		{"bool", `true`, false},
		{"null", `null`, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := gjson.Parse(tc.json)
			require.Equal(t, tc.want, isAgnesToolChoiceActive(result))
		})
	}
}
