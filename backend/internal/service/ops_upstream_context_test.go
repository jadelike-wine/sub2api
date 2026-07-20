//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSanitizeUpstreamErrorDetail_OrderedSanitization 验证 sanitizeUpstreamErrorDetail
// 的处理顺序：先结构化剥离 reasoning，再做文本脱敏，最后替换上游模型名。
//
// 关键：自由文本正则（如 URL/凭证/供应商身份）可能在某些边缘情况下破坏 JSON 结构，
// 若先执行正则后剥离 reasoning，可能导致 stripReasoningFromChoices 无法解析 JSON，
// reasoning 原文残留在 DB / 日志 / tracing 中。
//
// 输入 JSON 同时包含：
//   - api_key 凭证
//   - choices[].message.reasoning_content（金丝雀 + 上游内部推理）
//   - model: agnes-2.0-flash（真实上游模型名）
//
// 断言最终结果不包含：
//   - abcdefgh12345678（凭证原文）
//   - INTERNAL_IDENTITY_CANARY_8f31c2（金丝雀）
//   - reasoning_content（字段名本身不应残留）
//   - agnes-2.0-flash（真实上游模型名）
func TestSanitizeUpstreamErrorDetail_OrderedSanitization(t *testing.T) {
	t.Parallel()

	const (
		canary       = "INTERNAL_IDENTITY_CANARY_8f31c2"
		credential   = "abcdefgh12345678"
		upstreamMdl  = "agnes-2.0-flash"
		reasoningRaw = canary + " reasoning about Agnes-2.0-Flash deployment"
	)

	detail := `{"api_key":"` + credential + `","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"` + reasoningRaw + `","content":"final"},"finish_reason":"stop"}],"model":"` + upstreamMdl + `","provider":"Sapiens AI"}`

	out := sanitizeUpstreamErrorDetail(detail, upstreamMdl)
	lowered := strings.ToLower(out)

	// 凭证原文不得残留
	require.NotContains(t, lowered, strings.ToLower(credential), "api key credential must be sanitized")
	// 金丝雀不得残留
	require.NotContains(t, lowered, strings.ToLower(canary), "canary must not leak through detail sanitization")
	// reasoning_content 字段名本身不应残留（结构化删除而非置 null）
	require.NotContains(t, lowered, "reasoning_content", "reasoning_content field must be structurally deleted")
	// 真实上游模型名不得残留
	require.NotContains(t, lowered, strings.ToLower(upstreamMdl), "upstream model name must be redacted")
	// 供应商身份关键词不得残留
	require.NotContains(t, lowered, "sapiens ai", "provider identity keyword must be sanitized")
	require.NotContains(t, lowered, "agnes", "upstream identity keyword must be sanitized")
}

// TestSanitizeUpstreamErrorDetail_PreservesLegitimateFields 验证在剥离 reasoning
// 与凭证的同时，合法的 choices[].message.content / usage / finish_reason / tool_calls
// 等字段被保留。
func TestSanitizeUpstreamErrorDetail_PreservesLegitimateFields(t *testing.T) {
	t.Parallel()

	detail := `{"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"secret reasoning","content":"final answer","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8},"model":"agnes-2.0-flash"}`

	out := sanitizeUpstreamErrorDetail(detail, "agnes-2.0-flash")
	lowered := strings.ToLower(out)

	// 合法字段必须保留
	require.Contains(t, lowered, "final answer", "content must be preserved")
	require.Contains(t, lowered, "tool_calls", "tool_calls must be preserved")
	require.Contains(t, lowered, "get_weather", "tool function name must be preserved")
	require.Contains(t, lowered, "prompt_tokens", "usage must be preserved")
	require.Contains(t, lowered, "tool_calls", "finish_reason must be preserved")
	// 敏感字段必须剥离
	require.NotContains(t, lowered, "secret reasoning", "reasoning raw text must be stripped")
	require.NotContains(t, lowered, "reasoning_content", "reasoning_content field must be deleted")
}

// TestSanitizeUpstreamErrorDetail_NonJSONDetailSafeForNonJSONInput 验证非 JSON 输入
// （纯文本错误消息）仍能正确执行文本脱敏，不会因 JSON 检测失败而跳过凭证/模型名剥离。
func TestSanitizeUpstreamErrorDetail_NonJSONDetailSafeForNonJSONInput(t *testing.T) {
	t.Parallel()

	detail := `upstream agnes-2.0-flash returned 429 with api_key=abcdefgh12345678 at https://agnes.example.com`
	out := sanitizeUpstreamErrorDetail(detail, "agnes-2.0-flash")
	lowered := strings.ToLower(out)

	require.NotContains(t, lowered, "agnes-2.0-flash", "upstream model name must be redacted in plain text")
	require.NotContains(t, lowered, "abcdefgh12345678", "credential must be sanitized in plain text")
	require.NotContains(t, lowered, "https://agnes.example.com", "URL must be sanitized in plain text")
}

// TestSanitizeUpstreamErrorDetail_EmptyInputSafe 验证空输入不会 panic 或产生异常输出。
func TestSanitizeUpstreamErrorDetail_EmptyInputSafe(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", sanitizeUpstreamErrorDetail("", "agnes-2.0-flash"))
	require.Equal(t, "", sanitizeUpstreamErrorDetail("", ""))
	// 空白输入不被当作 JSON，原样返回（无敏感字段需要剥离）
	require.Equal(t, "   ", sanitizeUpstreamErrorDetail("   ", "agnes-2.0-flash"))
}

// TestSanitizeUpstreamErrorDetail_EmptyUpstreamModelSkipsRedaction 验证当 upstreamModel
// 为空时，redactUpstreamModelInMessage 跳过模型名替换，但其他脱敏仍执行。
func TestSanitizeUpstreamErrorDetail_EmptyUpstreamModelSkipsRedaction(t *testing.T) {
	t.Parallel()

	detail := `{"choices":[{"message":{"reasoning_content":"secret","content":"ok"}}],"model":"agnes-2.0-flash"}`
	out := sanitizeUpstreamErrorDetail(detail, "")
	lowered := strings.ToLower(out)

	// reasoning 仍剥离
	require.NotContains(t, lowered, "reasoning_content")
	require.NotContains(t, lowered, "secret")
	// content 保留
	require.Contains(t, lowered, "ok")
	// upstreamModel 为空时，model 字段中的 agnes-2.0-flash 仍会被供应商身份关键词正则替换（Agnes 关键字）
	require.NotContains(t, lowered, "agnes-2.0-flash")
}
