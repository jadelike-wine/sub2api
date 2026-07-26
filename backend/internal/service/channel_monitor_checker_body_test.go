//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// swapMonitorHTTPClient 临时替换 monitorHTTPClient 为不带 SSRF 校验的普通 client，
// 让 httptest (127.0.0.1) 能连通。测试结束后恢复。
func swapMonitorHTTPClient(t *testing.T) {
	t.Helper()
	orig := monitorHTTPClient
	monitorHTTPClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { monitorHTTPClient = orig })
}

// captureHandler 把每次收到的请求 body 和 headers 存起来，测试断言用。
type captureHandler struct {
	lastBody    map[string]any
	lastHeaders http.Header
	respondText string // 写到 Anthropic content[0].text 里（校验用）
	status      int
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.lastHeaders = r.Header.Clone()
	defer func() { _ = r.Body.Close() }()
	var parsed map[string]any
	_ = json.NewDecoder(r.Body).Decode(&parsed)
	h.lastBody = parsed

	if h.status == 0 {
		h.status = 200
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(h.status)
	// 构造 Anthropic 格式的响应：content[0].text = h.respondText
	_ = json.NewEncoder(w).Encode(map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": h.respondText},
		},
	})
}

func setupFakeAnthropic(t *testing.T, handler *captureHandler) string {
	t.Helper()
	swapMonitorHTTPClient(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

type openAICaptureHandler struct {
	lastBody                  map[string]any
	lastHeaders               http.Header
	lastPath                  string
	status                    int
	rawResponse               string
	responsesLeadingReasoning bool
}

func (h *openAICaptureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.lastHeaders = r.Header.Clone()
	h.lastPath = r.URL.Path
	defer func() { _ = r.Body.Close() }()
	var parsed map[string]any
	_ = json.NewDecoder(r.Body).Decode(&parsed)
	h.lastBody = parsed

	if h.status == 0 {
		h.status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(h.status)
	if h.rawResponse != "" {
		_, _ = w.Write([]byte(h.rawResponse))
		return
	}

	answer := answerFromOpenAIRequest(parsed)
	if h.lastPath == providerOpenAIResponsesPath {
		output := []map[string]any{}
		if h.responsesLeadingReasoning {
			output = append(output, map[string]any{
				"type":    "reasoning",
				"summary": []any{},
			})
		}
		output = append(output, map[string]any{
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{
				{"type": "output_text", "text": answer},
			},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": output,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"content": answer}}},
	})
}

func setupFakeOpenAI(t *testing.T, handler *openAICaptureHandler) string {
	t.Helper()
	swapMonitorHTTPClient(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

func answerFromOpenAIRequest(body map[string]any) string {
	prompt, _ := body["input"].(string)
	if prompt == "" {
		if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
			if msg, ok := messages[0].(map[string]any); ok {
				prompt, _ = msg["content"].(string)
			}
		}
	}
	return answerFromChallengePrompt(prompt)
}

var challengeQuestionRegex = regexp.MustCompile(`Q: (\d+) ([+-]) (\d+) = \?\nA:$`)

func answerFromChallengePrompt(prompt string) string {
	m := challengeQuestionRegex.FindStringSubmatch(prompt)
	if len(m) != 4 {
		return "0"
	}
	left, _ := strconv.Atoi(m[1])
	right, _ := strconv.Atoi(m[3])
	if m[2] == "+" {
		return strconv.Itoa(left + right)
	}
	return strconv.Itoa(left - right)
}

func TestRunCheckForModel_OffMode_PreservesDefaultBody(t *testing.T) {
	h := &captureHandler{respondText: "the answer is 42"}
	endpoint := setupFakeAnthropic(t, h)

	// 跑一次 off 模式（opts=nil），确认默认 body 行为未变
	_ = runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", nil)

	if h.lastBody["model"] != "claude-x" {
		t.Errorf("default body should contain model=claude-x, got %v", h.lastBody["model"])
	}
	if _, ok := h.lastBody["messages"]; !ok {
		t.Error("default body should contain messages")
	}
	if h.lastHeaders.Get("x-api-key") != "sk-fake" {
		t.Errorf("expected adapter's x-api-key header, got %q", h.lastHeaders.Get("x-api-key"))
	}
}

func TestRunCheckForModel_OpenAI_DefaultChatRequest(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-test", nil)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("default chat request should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	if h.lastPath != providerOpenAIPath {
		t.Fatalf("expected chat completions path %q, got %q", providerOpenAIPath, h.lastPath)
	}
	if h.lastBody["model"] != "gpt-test" {
		t.Errorf("chat body should contain model=gpt-test, got %v", h.lastBody["model"])
	}
	if _, ok := h.lastBody["messages"]; !ok {
		t.Error("chat body should contain messages")
	}
	if _, ok := h.lastBody["instructions"]; ok {
		t.Error("chat body must not contain top-level instructions")
	}
	if h.lastBody["stream"] != false {
		t.Errorf("chat body should set stream=false, got %v", h.lastBody["stream"])
	}
	if h.lastHeaders.Get("Authorization") != "Bearer sk-openai" {
		t.Errorf("expected bearer auth header, got %q", h.lastHeaders.Get("Authorization"))
	}
}

func TestGrokMonitorConfiguration(t *testing.T) {
	if err := validateProvider(MonitorProviderGrok); err != nil {
		t.Fatalf("grok provider should be supported: %v", err)
	}
	if got := normalizeMonitorPrimaryModel(MonitorProviderGrok, ""); got != MonitorDefaultGrokModel {
		t.Fatalf("expected default Grok model %q, got %q", MonitorDefaultGrokModel, got)
	}
	if err := validateAPIMode(MonitorProviderGrok, MonitorAPIModeChatCompletions); err != nil {
		t.Fatalf("grok chat_completions mode should be valid: %v", err)
	}
	if err := validateAPIMode(MonitorProviderGrok, MonitorAPIModeResponses); err == nil {
		t.Fatal("grok responses mode should be rejected by channel monitoring")
	}
	if err := validateReplaceRequestBody(MonitorProviderGrok, MonitorAPIModeChatCompletions, map[string]any{}); err == nil {
		t.Fatal("grok replace-mode body should require messages")
	}
}

func TestRunCheckForModel_Grok_DefaultChatRequest(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "xai-key", MonitorDefaultGrokModel, nil)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("Grok request should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	if res.LatencyMs == nil {
		t.Fatal("Grok request should record latency")
	}
	if h.lastPath != providerGrokPath {
		t.Fatalf("expected Grok chat completions path %q, got %q", providerGrokPath, h.lastPath)
	}
	if h.lastBody["model"] != MonitorDefaultGrokModel {
		t.Errorf("Grok body should contain model=%s, got %v", MonitorDefaultGrokModel, h.lastBody["model"])
	}
	if _, ok := h.lastBody["messages"]; !ok {
		t.Error("Grok body should contain messages")
	}
	if h.lastBody["stream"] != false {
		t.Errorf("Grok body should set stream=false, got %v", h.lastBody["stream"])
	}
	if h.lastHeaders.Get("Authorization") != "Bearer xai-key" {
		t.Errorf("expected Grok bearer auth header, got %q", h.lastHeaders.Get("Authorization"))
	}
}

func TestRunCheckForModel_Grok_UpstreamFailure(t *testing.T) {
	h := &openAICaptureHandler{status: http.StatusTooManyRequests}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "xai-key", MonitorDefaultGrokModel, nil)

	if res.Status != MonitorStatusError {
		t.Fatalf("Grok 429 should be recorded as error, got status=%s message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "upstream HTTP 429") {
		t.Fatalf("Grok failure should preserve upstream status, got %q", res.Message)
	}
	if res.LatencyMs == nil {
		t.Fatal("Grok failure should still record latency")
	}
}

func TestRunCheckForModel_Grok_RedactsXAIKeyFromUpstreamBody(t *testing.T) {
	h := &openAICaptureHandler{
		status:      http.StatusUnauthorized,
		rawResponse: `{"error":{"message":"invalid API key xai-secret"}}`,
	}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "request-key", MonitorDefaultGrokModel, nil)

	if res.Status != MonitorStatusError {
		t.Fatalf("Grok upstream failure should be recorded as error, got %s", res.Status)
	}
	if strings.Contains(res.Message, "xai-secret") {
		t.Fatalf("Grok error message leaked xAI key: %q", res.Message)
	}
	if !strings.Contains(res.Message, "xai-***REDACTED***") {
		t.Fatalf("Grok error message should contain redaction marker, got %q", res.Message)
	}
}

func TestRunCheckForModel_OpenAIResponses_DefaultRequest(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-test", &CheckOptions{
		APIMode: MonitorAPIModeResponses,
	})

	if res.Status != MonitorStatusOperational {
		t.Fatalf("default responses request should pass challenge, got status=%s message=%q", res.Status, res.Message)
	}
	if h.lastPath != providerOpenAIResponsesPath {
		t.Fatalf("expected responses path %q, got %q", providerOpenAIResponsesPath, h.lastPath)
	}
	if h.lastBody["model"] != "gpt-test" {
		t.Errorf("responses body should contain model=gpt-test, got %v", h.lastBody["model"])
	}
	instructions, _ := h.lastBody["instructions"].(string)
	if strings.TrimSpace(instructions) == "" {
		t.Error("responses body should contain non-empty instructions")
	}
	input, _ := h.lastBody["input"].(string)
	if strings.TrimSpace(input) == "" {
		t.Error("responses body should contain non-empty input")
	}
	if _, ok := h.lastBody["messages"]; ok {
		t.Error("responses body must not contain chat messages")
	}
	if h.lastBody["stream"] != false {
		t.Errorf("responses body should set stream=false, got %v", h.lastBody["stream"])
	}
	if h.lastHeaders.Get("Authorization") != "Bearer sk-openai" {
		t.Errorf("expected bearer auth header, got %q", h.lastHeaders.Get("Authorization"))
	}
}

func TestRunCheckForModel_OpenAIResponses_SkipsLeadingReasoningItem(t *testing.T) {
	h := &openAICaptureHandler{responsesLeadingReasoning: true}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-5.5", &CheckOptions{
		APIMode: MonitorAPIModeResponses,
	})

	if res.Status != MonitorStatusOperational {
		t.Fatalf("responses request should find text after leading reasoning item, got status=%s message=%q", res.Status, res.Message)
	}
	if h.lastPath != providerOpenAIResponsesPath {
		t.Fatalf("expected responses path %q, got %q", providerOpenAIResponsesPath, h.lastPath)
	}
}

func TestRunCheckForModel_OpenAIResponsesReplaceMissingInstructionsFailsLocally(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-openai", "gpt-test", &CheckOptions{
		APIMode:          MonitorAPIModeResponses,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride: map[string]any{
			"model": "gpt-test",
			"input": "hello",
		},
	})

	if res.Status != MonitorStatusError {
		t.Fatalf("invalid responses replace body should fail locally as error, got status=%s", res.Status)
	}
	if !strings.Contains(res.Message, "instructions and input are required") {
		t.Errorf("expected local validation message about instructions/input, got %q", res.Message)
	}
	if h.lastPath != "" {
		t.Errorf("invalid replace body should fail before HTTP request, got path %q", h.lastPath)
	}
}

func TestRunCheckForModel_MergeMode_UserFieldsWinButDenyListProtects(t *testing.T) {
	h := &captureHandler{respondText: "the answer is 42"}
	endpoint := setupFakeAnthropic(t, h)

	opts := &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeMerge,
		BodyOverride: map[string]any{
			"system":     "You are Claude Code...",
			"max_tokens": float64(999),   // 应该覆盖默认 50
			"model":      "hacked-model", // 应该被黑名单挡住，保留原 model
			"messages":   []any{},        // 同上，被挡
		},
		ExtraHeaders: map[string]string{
			"User-Agent":     "claude-cli/1.0",
			"Content-Length": "999", // 黑名单
			"x-custom":       "ok",
		},
	}
	_ = runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	if h.lastBody["system"] != "You are Claude Code..." {
		t.Errorf("merge mode should inject system, got %v", h.lastBody["system"])
	}
	// max_tokens 覆盖生效
	if mt, ok := h.lastBody["max_tokens"].(float64); !ok || mt != 999 {
		t.Errorf("merge mode should override max_tokens to 999, got %v", h.lastBody["max_tokens"])
	}
	// model 在黑名单 — 应该保留默认值
	if h.lastBody["model"] != "claude-x" {
		t.Errorf("model should be protected by deny list, got %v", h.lastBody["model"])
	}
	// messages 在黑名单 — 应该保留默认值（非空）
	msgs, _ := h.lastBody["messages"].([]any)
	if len(msgs) == 0 {
		t.Error("messages should be protected by deny list (kept default, non-empty)")
	}
	// header 合并
	if h.lastHeaders.Get("User-Agent") != "claude-cli/1.0" {
		t.Errorf("extra User-Agent should override, got %q", h.lastHeaders.Get("User-Agent"))
	}
	if h.lastHeaders.Get("x-custom") != "ok" {
		t.Errorf("extra custom header should be present, got %q", h.lastHeaders.Get("x-custom"))
	}
	// Content-Length 黑名单：会被 net/http 自动重算，但不应由用户的 "999" 决定。
	// 我们无法直接断言丢弃（http.Client 总会填上），只断言请求成功即可。
}

func TestRunCheckForModel_ReplaceMode_FullBodyUsedAndChallengeSkipped(t *testing.T) {
	// replace 模式下我们的 body 完全自定义，challenge 数学题不会出现在请求里，
	// 上游也不会回正确答案 — 但只要 2xx + 响应文本非空，就算 operational
	h := &captureHandler{respondText: "any non-empty text"}
	endpoint := setupFakeAnthropic(t, h)

	userBody := map[string]any{
		"model":      "user-forced-model",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"max_tokens": float64(10),
		"system":     "You are someone else",
	}
	opts := &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride:     userBody,
	}
	res := runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	// 请求 body = 用户提供的原样
	if h.lastBody["model"] != "user-forced-model" {
		t.Errorf("replace mode should use user's model, got %v", h.lastBody["model"])
	}
	if h.lastBody["system"] != "You are someone else" {
		t.Errorf("replace mode should use user's system, got %v", h.lastBody["system"])
	}
	// challenge 虽然没命中，但由于 replace 模式跳过 challenge 校验 + 响应非空 → operational
	if res.Status != MonitorStatusOperational {
		t.Errorf("replace mode with 2xx + non-empty text should be operational, got status=%s message=%q",
			res.Status, res.Message)
	}
}

func TestRunCheckForModel_ReplaceMode_EmptyResponseIsFailed(t *testing.T) {
	h := &captureHandler{respondText: ""} // 上游 200 但 content[0].text 为空
	endpoint := setupFakeAnthropic(t, h)

	opts := &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride:     map[string]any{"model": "x", "messages": []any{}},
	}
	res := runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	if res.Status != MonitorStatusFailed {
		t.Errorf("replace mode with empty text should be failed, got status=%s", res.Status)
	}
	if !strings.Contains(res.Message, "replace-mode") {
		t.Errorf("failure message should hint replace-mode, got %q", res.Message)
	}
}

// TestMonitorChallengeMaxTokens_DefaultIs50 验证默认 challenge 请求体的 max_tokens 为 50。
// 与 runChecksConcurrent 中强制注入的 chat_template_kwargs.enable_thinking=false 配合：
// 关闭 reasoning 后 arithmetic challenge 答案只需 1-2 个 token 即可返回，无需为
// reasoning_content 预留大预算。
//
// 覆盖全部 4 种 adapter：
//   - OpenAI Chat Completions: max_tokens=50
//   - Grok Chat Completions:   max_tokens=50
//   - Anthropic Messages:      max_tokens=50
//   - OpenAI Responses:        max_output_tokens=50（注意字段名不同）
func TestMonitorChallengeMaxTokens_DefaultIs50(t *testing.T) {
	if monitorChallengeMaxTokens != 50 {
		t.Fatalf("monitorChallengeMaxTokens const should be 50, got %d", monitorChallengeMaxTokens)
	}

	cases := []struct {
		name     string
		provider string
		model    string
		opts     *CheckOptions
		// bodyField 是请求体里 max token 字段名；不同 adapter 字段名不同。
		// OpenAI Responses 用 max_output_tokens，其它三个用 max_tokens。
		bodyField string
	}{
		{
			name:      "openai chat_completions",
			provider:  MonitorProviderOpenAI,
			model:     "agnes-2.0-flash",
			opts:      nil,
			bodyField: "max_tokens",
		},
		{
			name:      "grok chat_completions",
			provider:  MonitorProviderGrok,
			model:     MonitorDefaultGrokModel,
			opts:      nil,
			bodyField: "max_tokens",
		},
		{
			name:      "anthropic messages",
			provider:  MonitorProviderAnthropic,
			model:     "claude-x",
			opts:      nil,
			bodyField: "max_tokens",
		},
		{
			name:     "openai responses",
			provider: MonitorProviderOpenAI,
			model:    "gpt-test",
			opts: &CheckOptions{
				APIMode: MonitorAPIModeResponses,
			},
			bodyField: "max_output_tokens",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 两种 capture handler 都把最近一次请求 body 存到 lastBody（map[string]any）。
			// 用 any 持有指针，统一读取 lastBody。
			var handler interface {
				lastBodyMap() map[string]any
			}
			var endpoint string
			switch tc.provider {
			case MonitorProviderAnthropic:
				ch := &captureHandler{respondText: "42"}
				endpoint = setupFakeAnthropic(t, ch)
				handler = &captureHandlerView{ch}
			default:
				h := &openAICaptureHandler{}
				endpoint = setupFakeOpenAI(t, h)
				handler = &openAICaptureHandlerView{h}
			}
			_ = runCheckForModel(context.Background(), tc.provider, endpoint, "sk-fake", tc.model, tc.opts)

			body := handler.lastBodyMap()
			if body == nil {
				t.Fatalf("%s: captured request body is nil (handler never called?)", tc.name)
			}
			got, ok := body[tc.bodyField].(float64)
			if !ok {
				t.Fatalf("%s: expected %s to be number, got %T (%v)", tc.name, tc.bodyField, body[tc.bodyField], body[tc.bodyField])
			}
			if int(got) != 50 {
				t.Fatalf("%s: expected %s=50, got %v", tc.name, tc.bodyField, got)
			}
		})
	}
}

func TestExtractAnthropicMonitorText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "text block after thinking",
			body: `{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"2"}]}`,
			want: "2",
		},
		{
			name: "single text block",
			body: `{"content":[{"type":"text","text":"2"}]}`,
			want: "2",
		},
		{
			name: "thinking only",
			body: `{"content":[{"type":"thinking","thinking":""}]}`,
			want: "",
		},
		{
			name: "multiple text blocks",
			body: `{"content":[{"type":"text","text":"answer"},{"type":"tool_use","name":"x"},{"type":"text","text":"2"}]}`,
			want: "answer\n2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAnthropicMonitorText([]byte(tt.body))
			if got != tt.want {
				t.Fatalf("extractAnthropicMonitorText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// captureHandlerView / openAICaptureHandlerView 是为了让上面表驱动测试
// 用统一的 lastBodyMap() 接口读取两种 handler 的 lastBody 字段。
type captureHandlerView struct{ h *captureHandler }

func (v *captureHandlerView) lastBodyMap() map[string]any { return v.h.lastBody }

type openAICaptureHandlerView struct{ h *openAICaptureHandler }

func (v *openAICaptureHandlerView) lastBodyMap() map[string]any { return v.h.lastBody }

// TestRunCheckForModel_MergeMode_CanOverrideMaxTokens 验证 merge 模式下管理员
// 可覆盖默认 max token 预算。两种协议字段名不同都要覆盖：
//   - Chat Completions 用 {"max_tokens": 512}
//   - Responses      用 {"max_output_tokens": 512}
//
// 两字段都不在 bodyMergeKeyDenyList 中，因此都允许 merge 覆盖。
func TestRunCheckForModel_MergeMode_CanOverrideMaxTokens(t *testing.T) {
	cases := []struct {
		name      string
		apiMode   string
		bodyField string
	}{
		{name: "chat_completions uses max_tokens", apiMode: MonitorAPIModeChatCompletions, bodyField: "max_tokens"},
		{name: "responses uses max_output_tokens", apiMode: MonitorAPIModeResponses, bodyField: "max_output_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &openAICaptureHandler{}
			endpoint := setupFakeOpenAI(t, h)

			opts := &CheckOptions{
				APIMode:          tc.apiMode,
				BodyOverrideMode: MonitorBodyOverrideModeMerge,
				BodyOverride:     map[string]any{tc.bodyField: float64(512)},
			}
			_ = runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-fake", "agnes-2.0-flash", opts)

			got, ok := h.lastBody[tc.bodyField].(float64)
			if !ok {
				t.Fatalf("expected %s to be number, got %T (%v)", tc.bodyField, h.lastBody[tc.bodyField], h.lastBody[tc.bodyField])
			}
			if int(got) != 512 {
				t.Fatalf("merge mode should allow overriding %s to 512, got %v", tc.bodyField, got)
			}
		})
	}
}

// TestRunCheckForModel_ReasoningTruncation_ReturnsExplicitMessage 验证 2xx + 空 content +
// finish_reason=length + 非空 reasoning_content 时返回明确脱敏提示而非通用 challenge mismatch。
// 复现 Agnes agnes-2.0-flash 在 max_tokens 过小时的监控误报场景。
func TestRunCheckForModel_ReasoningTruncation_ReturnsExplicitMessage(t *testing.T) {
	raw := `{
		"choices": [{
			"finish_reason": "length",
			"message": {
				"content": "",
				"reasoning_content": "let me compute: 17 + 25 = 42, so the answer is 42."
			}
		}],
		"usage": {
			"completion_tokens": 50,
			"completion_tokens_details": {"reasoning_tokens": 50}
		}
	}`
	h := &openAICaptureHandler{rawResponse: raw}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-fake", "agnes-2.0-flash", nil)

	if res.Status != MonitorStatusFailed {
		t.Fatalf("reasoning-truncated response should still be failed, got status=%s", res.Status)
	}
	if !strings.Contains(res.Message, "response truncated") {
		t.Fatalf("expected explicit truncation hint, got %q", res.Message)
	}
	if !strings.Contains(res.Message, "reasoning consumed max_tokens") {
		t.Fatalf("expected hint to mention reasoning consumed max_tokens, got %q", res.Message)
	}
	if strings.Contains(res.Message, "challenge mismatch") {
		t.Fatalf("should not fall back to generic challenge mismatch, got %q", res.Message)
	}
	// 提示必须脱敏，不能把 reasoning_content 原文带进 message
	if strings.Contains(res.Message, "let me compute") {
		t.Fatalf("message should not leak reasoning_content, got %q", res.Message)
	}
}

// TestRunCheckForModel_NormalResponseWithAnswerIsOperational 验证带正确答案的正常响应仍为 operational，
// 并回归默认 max_tokens=50（与 runChecksConcurrent 中强制注入的 chat_template_kwargs.enable_thinking=false
// 配合，关闭 reasoning 后小预算即可返回 content）。
func TestRunCheckForModel_NormalResponseWithAnswerIsOperational(t *testing.T) {
	// openAICaptureHandler 默认按 challenge 算式回正确答案到 choices.0.message.content
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-fake", "agnes-2.0-flash", nil)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("normal response with correct answer should be operational, got status=%s message=%q", res.Status, res.Message)
	}
	mt, ok := h.lastBody["max_tokens"].(float64)
	if !ok {
		t.Fatalf("expected max_tokens to be number, got %T (%v)", h.lastBody["max_tokens"], h.lastBody["max_tokens"])
	}
	if int(mt) != 50 {
		t.Errorf("expected default max_tokens=50, got %v", mt)
	}
}

// TestChallengeMismatchMessage_FallbackToGeneric 验证非 reasoning-truncation 场景
// 仍回退到通用 challenge mismatch 提示（含 expected / got）。
func TestChallengeMismatchMessage_FallbackToGeneric(t *testing.T) {
	cases := []struct {
		name     string
		rawBody  string
		respText string
		expected string
		wantSub  string
	}{
		{
			name:     "empty content but no reasoning_content",
			rawBody:  `{"choices":[{"finish_reason":"length","message":{"content":""}}]}`,
			respText: "",
			expected: "42",
			wantSub:  "challenge mismatch",
		},
		{
			name:     "empty content but finish_reason=stop",
			rawBody:  `{"choices":[{"finish_reason":"stop","message":{"content":"","reasoning_content":"..."}}]}`,
			respText: "",
			expected: "42",
			wantSub:  "challenge mismatch",
		},
		{
			name:     "wrong answer in content",
			rawBody:  `{"choices":[{"finish_reason":"stop","message":{"content":"99"}}]}`,
			respText: "99",
			expected: "42",
			wantSub:  "challenge mismatch",
		},
		{
			name:     "non-openai shape (anthropic-style)",
			rawBody:  `{"content":[{"type":"text","text":""}]}`,
			respText: "",
			expected: "42",
			wantSub:  "challenge mismatch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := challengeMismatchMessage(tc.rawBody, tc.respText, tc.expected)
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("expected message to contain %q, got %q", tc.wantSub, got)
			}
		})
	}
}

// TestApplyDefaultThinkingOverride 验证 runChecksConcurrent 默认注入
// chat_template_kwargs.enable_thinking=false 的行为：
//   - nil opts 不 panic
//   - replace 模式跳过注入（保留用户全权自管 body 的语义）
//   - off / 空 / merge 模式统一改写为 merge，并注入 enable_thinking=false
//   - 用户已有的 chat_template_kwargs.enable_thinking 优先（不被覆盖）
//   - 用户已有的 chat_template_kwargs 其他字段保留
func TestApplyDefaultThinkingOverride(t *testing.T) {
	t.Run("nil opts is no-op", func(t *testing.T) {
		applyDefaultThinkingOverride(nil)
		// 不 panic 即通过
	})

	t.Run("replace mode skips injection", func(t *testing.T) {
		opts := &CheckOptions{
			BodyOverrideMode: MonitorBodyOverrideModeReplace,
			BodyOverride:     map[string]any{"model": "x", "messages": []any{}},
		}
		applyDefaultThinkingOverride(opts)
		if opts.BodyOverrideMode != MonitorBodyOverrideModeReplace {
			t.Fatalf("replace mode must be preserved, got %q", opts.BodyOverrideMode)
		}
		if _, ok := opts.BodyOverride["chat_template_kwargs"]; ok {
			t.Fatal("replace mode must not inject chat_template_kwargs")
		}
	})

	t.Run("off mode is rewritten to merge with injection", func(t *testing.T) {
		opts := &CheckOptions{
			BodyOverrideMode: MonitorBodyOverrideModeOff,
		}
		applyDefaultThinkingOverride(opts)
		if opts.BodyOverrideMode != MonitorBodyOverrideModeMerge {
			t.Fatalf("off mode should be rewritten to merge, got %q", opts.BodyOverrideMode)
		}
		cfg, ok := opts.BodyOverride["chat_template_kwargs"].(map[string]any)
		if !ok {
			t.Fatalf("expected chat_template_kwargs map injected, got %T", opts.BodyOverride["chat_template_kwargs"])
		}
		if cfg["enable_thinking"] != false {
			t.Errorf("expected enable_thinking=false, got %v", cfg["enable_thinking"])
		}
	})

	t.Run("empty mode is rewritten to merge with injection", func(t *testing.T) {
		opts := &CheckOptions{}
		applyDefaultThinkingOverride(opts)
		if opts.BodyOverrideMode != MonitorBodyOverrideModeMerge {
			t.Fatalf("empty mode should be rewritten to merge, got %q", opts.BodyOverrideMode)
		}
		if _, ok := opts.BodyOverride["chat_template_kwargs"]; !ok {
			t.Fatal("expected chat_template_kwargs injected for empty mode")
		}
	})

	t.Run("merge mode preserves existing enable_thinking=true", func(t *testing.T) {
		opts := &CheckOptions{
			BodyOverrideMode: MonitorBodyOverrideModeMerge,
			BodyOverride: map[string]any{
				"chat_template_kwargs": map[string]any{"enable_thinking": true},
			},
		}
		applyDefaultThinkingOverride(opts)
		cfg, ok := opts.BodyOverride["chat_template_kwargs"].(map[string]any)
		if !ok {
			t.Fatalf("expected chat_template_kwargs map preserved, got %T", opts.BodyOverride["chat_template_kwargs"])
		}
		if cfg["enable_thinking"] != true {
			t.Errorf("user-provided enable_thinking=true must win, got %v", cfg["enable_thinking"])
		}
	})

	t.Run("merge mode preserves existing chat_template_kwargs siblings", func(t *testing.T) {
		opts := &CheckOptions{
			BodyOverrideMode: MonitorBodyOverrideModeMerge,
			BodyOverride: map[string]any{
				"chat_template_kwargs": map[string]any{"temperature": float64(0.7)},
			},
		}
		applyDefaultThinkingOverride(opts)
		cfg, ok := opts.BodyOverride["chat_template_kwargs"].(map[string]any)
		if !ok {
			t.Fatalf("expected chat_template_kwargs map preserved, got %T", opts.BodyOverride["chat_template_kwargs"])
		}
		if cfg["temperature"] != float64(0.7) {
			t.Errorf("user-provided temperature must be preserved, got %v", cfg["temperature"])
		}
		if cfg["enable_thinking"] != false {
			t.Errorf("expected enable_thinking=false injected, got %v", cfg["enable_thinking"])
		}
	})

	t.Run("merge mode preserves other user override fields", func(t *testing.T) {
		opts := &CheckOptions{
			BodyOverrideMode: MonitorBodyOverrideModeMerge,
			BodyOverride: map[string]any{
				"max_tokens": float64(100),
				"system":     "you are a monitor",
			},
		}
		applyDefaultThinkingOverride(opts)
		if opts.BodyOverride["max_tokens"] != float64(100) {
			t.Errorf("user max_tokens must be preserved, got %v", opts.BodyOverride["max_tokens"])
		}
		if opts.BodyOverride["system"] != "you are a monitor" {
			t.Errorf("user system must be preserved, got %v", opts.BodyOverride["system"])
		}
	})
}

// TestRunChecksConcurrent_InjectsDisableThinkingOverride 是端到端验证：
// 通过 ChannelMonitorService.runChecksConcurrent 触发的请求必须包含
// chat_template_kwargs.enable_thinking=false（用户未显式配置时）。
func TestRunChecksConcurrent_InjectsDisableThinkingOverride(t *testing.T) {
	h := &openAICaptureHandler{}
	endpoint := setupFakeOpenAI(t, h)

	svc := &ChannelMonitorService{}
	monitor := &ChannelMonitor{
		ID:               1,
		Provider:         MonitorProviderOpenAI,
		Endpoint:         endpoint,
		APIKey:           "sk-fake",
		PrimaryModel:     "gpt-test",
		BodyOverrideMode: MonitorBodyOverrideModeOff,
	}

	_ = svc.runChecksConcurrent(context.Background(), monitor)

	if h.lastBody == nil {
		t.Fatal("expected request body captured, got nil (handler never called?)")
	}
	cfg, ok := h.lastBody["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatalf("expected chat_template_kwargs injected into request body, got %T (%v)",
			h.lastBody["chat_template_kwargs"], h.lastBody["chat_template_kwargs"])
	}
	if cfg["enable_thinking"] != false {
		t.Errorf("expected enable_thinking=false in request body, got %v", cfg["enable_thinking"])
	}
	// monitorChallengeMaxTokens=50 也应一并出现在请求体中
	if mt, ok := h.lastBody["max_tokens"].(float64); !ok || int(mt) != 50 {
		t.Errorf("expected max_tokens=50, got %v", h.lastBody["max_tokens"])
	}
}

func TestValidateChallenge_AnthropicTextAfterThinking(t *testing.T) {
	body := []byte(`{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"答案是 2"}]}`)
	respText := extractAnthropicMonitorText(body)

	if !validateChallenge(respText, "2") {
		t.Fatalf("validateChallenge(%q, %q) = false, want true", respText, "2")
	}
}
