package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// openaiCCRawAllowedHeaders 是 CC 直转路径专用的客户端 header 透传白名单。
//
// **关键**：不能复用 openaiAllowedHeaders——后者含 Codex 客户端专属 header
// （originator / session_id / x-codex-turn-state / x-codex-turn-metadata / conversation_id），
// 这些在 ChatGPT OAuth 上游是必需的，但透传给 DeepSeek/Kimi/GLM 等第三方
// OpenAI 兼容上游会造成：
//   - 完全忽略（多数友好厂商）——隐性污染上游统计
//   - 400 "unknown parameter"（严格上游）——可见错误
//
// 这里仅放行通用 HTTP header；content-type / authorization / accept 由上下文
// 显式设置，不依赖透传。
//
// 参见决策记录：
// pensieve/short-term/maxims/dont-reuse-shared-headers-whitelist-across-different-upstream-trust-domains
var openaiCCRawAllowedHeaders = map[string]bool{
	"accept-language": true,
	"user-agent":      true,
}

// forwardAsRawChatCompletions 直转客户端的 Chat Completions 请求到上游
// `{base_url}/v1/chat/completions`，**不**做 CC↔Responses 协议转换。
//
// 适用场景：account.platform=openai && account.type=apikey && 上游已被探测确认
// 不支持 /v1/responses 端点（如 DeepSeek/Kimi/GLM/Qwen 等第三方 OpenAI 兼容上游）。
//
// 与 ForwardAsChatCompletions 的关键差异：
//
//   - 不调用 apicompat.ChatCompletionsToResponses，body 仅做模型 ID 改写
//   - 上游 URL 拼到 /v1/chat/completions 而非 /v1/responses
//   - 流式响应 SSE 直接透传给客户端（上游 chunk 已是 CC 格式）
//   - 非流式响应 JSON 直接透传，仅按需提取 usage
//   - 不应用 codex OAuth transform（APIKey 路径无 OAuth）
//   - 不注入 prompt_cache_key（OAuth 专属机制）
//
// 调用入口：openai_gateway_chat_completions.go::ForwardAsChatCompletions
// 在函数顶部按 openai_compat.ShouldUseResponsesAPI 分流。
func (s *OpenAIGatewayService) forwardAsRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	// 1. Parse minimal fields needed for routing/billing
	originalModel := gjson.GetBytes(body, "model").String()
	if originalModel == "" {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()

	// 1b. Extract service tier from the raw body before any transformation.
	serviceTier := extractOpenAIServiceTierFromBody(body)

	// 2. Resolve model mapping (same as ForwardAsChatCompletions)
	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	grokCacheIdentity := ""
	if account.Platform == PlatformGrok {
		// Resolve before image bridging or other body rewrites so the fallback is
		// anchored to the client's stable conversation prefix.
		grokCacheIdentity = resolveGrokCacheIdentity(c, body, "", upstreamModel)
	}
	reasoningEffort := extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	// 国产模型默认 effort 补充：需要 mappedModel 判定，推迟到 billingModel 算出之后。
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, billingModel)

	// 3. Rewrite model in body (no protocol conversion)
	upstreamBody := body
	if upstreamModel != originalModel {
		upstreamBody = ReplaceModelInBody(body, upstreamModel)
	}
	if normalizedBody, normalized := NormalizeGLMOpenAIReasoningEffort(upstreamBody, upstreamModel); normalized {
		upstreamBody = normalizedBody
	}

	// DeepSeek V4 / SenseNova thinking 适配（OpenAI CC 直转路径）：
	// 客户端可能混合发送 Anthropic 风格的 thinking.type=adaptive 或 output_config.effort，
	// 不同上游对 thinking.type 的接受值不同：
	//   - SenseNova: 接受 enabled|disabled|auto，不接受 adaptive
	//   - Native DeepSeek V4: 接受 enabled|disabled|adaptive，不接受 auto
	// 此处复用统一适配入口，确保流式/非流式行为一致。
	if isDeepSeekV4Model(upstreamModel) {
		rewritten, _, err := NormalizeDeepSeekV4ThinkingForAccount(account, upstreamModel, upstreamBody)
		if err != nil {
			writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return nil, err
		}
		if !bytes.Equal(rewritten, upstreamBody) {
			upstreamBody = rewritten
		}
	}

	// 3b. 动态注入内部身份提示词（隐藏真实上游模型）。
	// publicModel 必须来自服务端解析后的 ResolvedModel.PublicModel，
	// 由 handler 在调用 ForwardAsChatCompletions 之前通过 c.Set 注入。
	// 未注入时 fallback 到 originalModel（即 body.model，已被渠道映射改写为上游模型），
	// 此时**不注入**提示词，保持向后兼容（避免破坏 DeepSeek 等模型的 reasoning_content 回放顺序）。
	publicModelFromContext := getPublicModelFromContext(c)
	publicModel := publicModelFromContext
	if publicModel == "" {
		publicModel = originalModel
	}
	if publicModelFromContext != "" {
		if injected, injErr := injectIdentitySystemPrompt(upstreamBody, publicModelFromContext); injErr == nil {
			upstreamBody = injected
		}
	}

	// 3c. Agnes 上游请求规范化（服务端 Thinking 策略 + 剥离客户端绕过字段）。
	// 仅对 Agnes 上游账号生效（通过 IsAgnesProvider 标识，独立于图片适配能力）：
	// 纯文本 Agnes 账号（agnes_provider=true 但 agnes_chat_image_adapter=false）
	// 也必须执行 Thinking 规范化。
	// 客户端传入的 chat_template_kwargs.enable_thinking / include_reasoning /
	// return_reasoning / expose_reasoning / 顶层 thinking / reasoning_effort 等
	// 字段都会被剥离或覆盖，最终值由服务端配置决定。
	// 不修改 model 字段（已由 step 3 通过 ResolvedModel 处理）。
	// 不依赖客户端值：即便客户端传入 null / 字符串 / 数组 / 异常对象，也不会 panic。
	if account.IsAgnesProvider() && s.cfg != nil {
		normalizedBody, normRes := normalizeAgnesThinkingRequest(upstreamBody, s.cfg.AgnesChat.Thinking)
		if normRes.Applied {
			upstreamBody = normalizedBody
			logger.L().Debug("agnes thinking request normalized",
				zap.Int64("account_id", account.ID),
				zap.String("mode", normRes.Mode),
				zap.Bool("effective_enable_thinking", normRes.EffectiveEnableThinking),
				zap.Bool("client_had_enable_thinking", normRes.ClientHadEnableThinking),
				zap.Bool("client_enable_thinking_value", normRes.ClientEnableThinkingValue),
				zap.Strings("stripped_bypass_fields", normRes.StrippedBypassFields),
				zap.Bool("stripped_anthropic_thinking", normRes.StrippedAnthropicThinking),
				zap.Strings("auto_triggered_signals", normRes.AutoTriggeredSignals),
			)
		}
	}

	// 4. Apply OpenAI fast policy on the CC body
	updatedBody, policyErr := s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, upstreamBody)
	if policyErr != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(policyErr, &blocked) {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			writeChatCompletionsError(c, http.StatusForbidden, "permission_error", blocked.Message)
		}
		return nil, policyErr
	}
	upstreamBody = updatedBody

	// Agnes 2.0 Flash 多模态聊天图片适配：
	// 仅对 OpenAI APIKey 账号 + Extra["agnes_chat_image_adapter"]=true 生效，
	// 将下游 data:image/...;base64 图片上传到 R2，替换为公网 HTTPS URL。
	// 公网 HTTPS URL 透传不改写；非法 URL/超限图片返回 OpenAI 风格 invalid_request_error。
	if account.AgnesChatImageAdapterEnabled() && s.agnesChatImageAdapter != nil {
		adapted, adapterErr := s.agnesChatImageAdapter.AdaptBody(ctx, c, upstreamBody)
		if adapterErr != nil {
			var adapterErrTyped *AgnesChatImageAdapterError
			if errors.As(adapterErr, &adapterErrTyped) {
				writeChatCompletionsError(c, adapterErrTyped.StatusCode, adapterErrTyped.ErrType, adapterErrTyped.Message)
			} else {
				writeChatCompletionsError(c, http.StatusBadGateway, "api_error", "Agnes chat image adapter failed")
			}
			return nil, adapterErr
		}
		upstreamBody = adapted
	}

	// Grok Composer does not accept image_url parts directly, but Grok Build
	// can describe the images first. Bridge only this exact failure mode.
	token, tokenKind, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("account %d missing %s credential", account.ID, tokenKind)
	}

	var bridgeUsage OpenAIUsage
	if account.Platform == PlatformGrok {
		bridgedBody, usage, bridged, bridgeErr := s.bridgeGrokComposerImageInputs(ctx, c, account, upstreamBody, token)
		if bridgeErr != nil {
			var failoverErr *UpstreamFailoverError
			if !errors.As(bridgeErr, &failoverErr) && c != nil && c.Writer != nil && !c.Writer.Written() {
				writeChatCompletionsError(c, http.StatusBadGateway, "upstream_error", bridgeErr.Error())
			}
			return nil, bridgeErr
		}
		if bridged {
			upstreamBody = bridgedBody
			addOpenAIUsage(&bridgeUsage, usage)
		}
	}

	if clientStream {
		var usageErr error
		upstreamBody, usageErr = ensureOpenAIChatStreamUsage(upstreamBody)
		if usageErr != nil {
			return nil, fmt.Errorf("enable stream usage: %w", usageErr)
		}
	}
	if account.Platform == PlatformGrok {
		upstreamBody, err = stripGrokChatPromptCacheKey(upstreamBody)
		if err != nil {
			return nil, fmt.Errorf("remove Responses-only Grok prompt cache key: %w", err)
		}
		upstreamBody, err = normalizeGrokChatReasoningEffort(upstreamBody, upstreamModel)
		if err != nil {
			return nil, fmt.Errorf("normalize Grok chat reasoning effort: %w", err)
		}
	}

	logger.L().Debug("openai chat_completions raw: forwarding without protocol conversion",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	// 5. Build and send upstream request via the shared CC pipeline
	targetURL, err := s.rawChatCompletionsURL(account)
	if err != nil {
		return nil, err
	}
	SetActualOpenAIUpstreamEndpoint(c, grokChatRawEndpoint)
	customUA := account.GetOpenAIUserAgent()
	if customUA == "" && account.IsGrokOAuth() {
		customUA = "sub2api-grok/1.0"
	}
	resp, err := s.sendCCUpstreamRequest(ctx, c, account, targetURL, upstreamBody, clientStream, token, customUA, grokCacheIdentity)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// 7. Handle error response with failover
	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if account.Platform == PlatformGrok {
			kind := "http_error"
			if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
				kind = "failover"
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
				Kind:               kind,
				Message:            upstreamMsg,
			})
			s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
			if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
				return nil, &UpstreamFailoverError{
					StatusCode:             resp.StatusCode,
					ResponseBody:           respBody,
					ResponseHeaders:        resp.Header.Clone(),
					RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
				}
			}
			return s.handleChatCompletionsErrorResponse(resp, c, account, billingModel)
		}
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return s.handleChatCompletionsErrorResponse(resp, c, account, billingModel)
	}

	if account.Platform == PlatformGrok {
		s.updateGrokUsageFromResponse(ctx, account, resp.Header, resp.StatusCode)
	}

	// 8. Forward response
	var result *OpenAIForwardResult
	var forwardErr error
	if clientStream {
		result, forwardErr = s.streamRawChatCompletions(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime, len(body))
	} else {
		result, forwardErr = s.bufferRawChatCompletions(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	if result != nil {
		addOpenAIUsage(&result.Usage, bridgeUsage)
		result.UpstreamEndpoint = grokChatRawEndpoint
	}
	return result, forwardErr
}

func (s *OpenAIGatewayService) rawChatCompletionsURL(account *Account) (string, error) {
	if account.Platform == PlatformGrok {
		targetURL, err := buildGrokChatCompletionsURL(account, s.cfg, s.settingService)
		if err != nil {
			return "", fmt.Errorf("invalid grok base_url: %w", err)
		}
		return targetURL, nil
	}

	return s.openAIChatCompletionsTargetURL(account)
}

// streamRawChatCompletions 透传上游 CC SSE 流到客户端，并提取 usage（包括
// 末尾 [DONE] 之前的 chunk 中的 usage 字段，按 OpenAI CC 协议）。
//
// usage 字段仅在客户端请求 stream_options.include_usage=true 时出现于上游响应中。
// 网关会对上游强制打开 include_usage 以保证计费完整，并原样向下游透传 usage，
// 让级联代理或下游计费系统也能拿到完整用量。
//
// 脱敏：每个 SSE data: <json> 行在写出前都会被解析-修改-重新序列化，
// 重写 model 字段为 publicModel 并删除 provider_specific_fields / metadata。
// [DONE] 哨兵、注释行、event 行原样透传。
func (s *OpenAIGatewayService) streamRawChatCompletions(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
	requestBodyLen int,
) (*OpenAIForwardResult, error) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)
	scanner := s.newUpstreamSSEScanner(resp.Body)

	// publicModel 用于客户端响应的 model 字段（隐藏真实上游模型）。
	// 优先从 gin.Context 读取（由 handler 注入）；未注入时 fallback 到 originalModel。
	publicModel := getPublicModelFromContext(c)
	if publicModel == "" {
		publicModel = originalModel
	}
	// 普通 C 端 OpenAI 兼容接口策略：响应层始终剥离 reasoning 别名。
	// 不依赖 account.AgnesChatImageAdapterEnabled() —— 该字段仅控制请求阶段图片适配，
	// 与响应层安全策略语义无关。即便上游是 DeepSeek-reasoner，客户端也不得看到
	// 原始 reasoning（上游在多轮对话中需要的 reasoning_content 回放由请求层透传实现）。

	var usage OpenAIUsage
	var firstTokenMs *int
	clientDisconnected := false
	clientOutputStarted := false
	pendingLines := make([]string, 0, 8)
	refusalDetector := newOpenAIChatSilentRefusalDetector(requestBodyLen)
	// streamFatalErr 一旦非空表示已发生不可恢复的解析失败，需要终止流并返回受控错误。
	var streamFatalErr error
	// chunkIndex 用于在 malformed JSON 警告日志中标识 chunk 序号（不含原文）。
	chunkIndex := 0

	writeLine := func(line string) {
		if clientDisconnected || streamFatalErr != nil {
			return
		}
		// 在写出前对 SSE data 行做脱敏（重写 model、删除上游扩展字段、剥离 reasoning）。
		// 非 data 行（注释、event、空行）与 [DONE] 原样透传。
		redactedLine, result := redactOpenAIChatSSELine(line, publicModel)
		switch result {
		case SSELineFatal:
			// Fail-closed：malformed JSON 不得透传原文。
			// 记录不含原文的警告日志（仅记 chunk 序号与 request_id），终止流。
			logger.L().Warn("openai chat_completions raw: malformed SSE JSON, terminating stream (fail-closed)",
				zap.String("request_id", requestID),
				zap.Int("chunk_index", chunkIndex),
				zap.Int64("account_id", account.ID),
			)
			streamFatalErr = fmt.Errorf("malformed upstream SSE JSON at chunk %d", chunkIndex)
			return
		case SSELineDrop:
			// reasoning-only chunk 删除后无有效载荷，丢弃。
			chunkIndex++
			return
		}
		if !clientOutputStarted && !refusalDetector.ShouldReleaseClientOutput() {
			pendingLines = append(pendingLines, redactedLine)
			chunkIndex++
			return
		}
		if !clientOutputStarted {
			writeStreamHeaders()
			for _, pending := range pendingLines {
				if _, werr := c.Writer.WriteString(pending + "\n"); werr != nil {
					clientDisconnected = true
					logger.L().Debug("openai chat_completions raw: client disconnected, continuing to drain upstream for billing",
						zap.Error(werr),
						zap.String("request_id", requestID),
					)
					return
				}
			}
			pendingLines = pendingLines[:0]
			clientOutputStarted = true
		}
		if _, werr := c.Writer.WriteString(redactedLine + "\n"); werr != nil {
			clientDisconnected = true
			logger.L().Debug("openai chat_completions raw: client disconnected, continuing to drain upstream for billing",
				zap.Error(werr),
				zap.String("request_id", requestID),
			)
		}
		chunkIndex++
	}

	for scanner.Scan() {
		line := scanner.Text()
		refusalDetector.ObserveSSELine(line)
		if payload, ok := extractOpenAISSEDataLine(line); ok {
			trimmedPayload := strings.TrimSpace(payload)
			if trimmedPayload != "[DONE]" {
				observer.ObserveOpenAI([]byte(payload), strings.TrimSpace(gjson.Get(payload, "type").String()))
				usageOnlyChunk := isOpenAIChatUsageOnlyStreamChunk(payload)
				if u := extractCCStreamUsage(payload); u != nil {
					usage = *u
				}
				if firstTokenMs == nil && !usageOnlyChunk {
					elapsed := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &elapsed
				}
			}
		}

		writeLine(line)
		if streamFatalErr != nil {
			// Fail-closed：终止扫描，不再读取后续 chunk。
			break
		}
		if line == "" {
			if !clientDisconnected && clientOutputStarted {
				c.Writer.Flush()
			}
			continue
		}
		if !clientDisconnected && clientOutputStarted {
			c.Writer.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("openai chat_completions raw: stream read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	} else if streamFatalErr == nil && !clientDisconnected && !clientOutputStarted {
		if refusalDetector.IsSilentRefusal() {
			return nil, newOpenAISilentRefusalFailoverError(c, account, requestID)
		}
		if len(pendingLines) > 0 {
			writeStreamHeaders()
			for _, pending := range pendingLines {
				if _, werr := c.Writer.WriteString(pending + "\n"); werr != nil {
					clientDisconnected = true
					logger.L().Debug("openai chat_completions raw: client disconnected during final flush",
						zap.Error(werr),
						zap.String("request_id", requestID),
					)
					break
				}
			}
			if !clientDisconnected {
				c.Writer.Flush()
				clientOutputStarted = true
			}
		}
	}

	// Fail-closed：malformed JSON 时向客户端发送受控 SSE 错误事件并终止流。
	// 不泄露原始 payload、不含上游模型名/供应商信息。
	if streamFatalErr != nil && !clientDisconnected {
		if !clientOutputStarted {
			writeStreamHeaders()
		}
		_, _ = c.Writer.WriteString("data: {\"error\":{\"message\":\"upstream stream parse error\",\"type\":\"upstream_stream_error\"}}\n\n")
		_, _ = c.Writer.WriteString("data: [DONE]\n\n")
		if !clientDisconnected {
			c.Writer.Flush()
		}
	}

	return &OpenAIForwardResult{
		RequestID:                     requestID,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		ReasoningEffort:               reasoningEffort,
		ServiceTier:                   serviceTier,
		Stream:                        true,
		Duration:                      time.Since(startTime),
		FirstTokenMs:                  firstTokenMs,
	}, nil
}

// ensureOpenAIChatStreamUsage 确保 raw Chat Completions 流式请求会让上游返回 usage。
// usage 也会继续向下游透传，支持级联代理和下游计费系统。
func ensureOpenAIChatStreamUsage(body []byte) ([]byte, error) {
	updated, err := sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return body, err
	}
	return updated, nil
}

func isOpenAIChatUsageOnlyStreamChunk(payload string) bool {
	if strings.TrimSpace(payload) == "" {
		return false
	}
	if !gjson.Get(payload, "usage").Exists() {
		return false
	}
	choices := gjson.Get(payload, "choices")
	return choices.Exists() && choices.IsArray() && len(choices.Array()) == 0
}

// extractCCStreamUsage 从单个 CC 流式 chunk 的 payload 中提取 usage 字段。
// CC 协议中 usage 仅出现在末尾 chunk（且仅当 include_usage 生效时），
// 但上游可能在多个 chunk 中重复——总是用最新值。
func extractCCStreamUsage(payload string) *OpenAIUsage {
	usageResult := gjson.Get(payload, "usage")
	if !usageResult.Exists() || !usageResult.IsObject() {
		return nil
	}
	u, ok := openAIUsageFromGJSON(usageResult)
	if !ok {
		return nil
	}
	return &u
}

// bufferRawChatCompletions 透传上游 CC 非流式 JSON 响应，并在写出前做脱敏：
//   - 重写顶层 model 字段为 publicModel（隐藏真实上游模型）
//   - 删除 provider_specific_fields
//   - 删除 metadata
//
// 保留标准 OpenAI 兼容字段：id / object / created / model / choices / usage。
func (s *OpenAIGatewayService) bufferRawChatCompletions(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			writeChatCompletionsError(c, http.StatusBadGateway, "api_error", "Failed to read upstream response")
		}
		return nil, fmt.Errorf("read upstream body: %w", err)
	}
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	observer.ObserveOpenAI(respBody, strings.TrimSpace(gjson.GetBytes(respBody, "type").String()))

	var usage OpenAIUsage
	if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsedUsage
	}

	// publicModel 用于客户端响应的 model 字段（隐藏真实上游模型）。
	// 优先从 gin.Context 读取（由 handler 注入）；未注入时 fallback 到 originalModel。
	publicModel := getPublicModelFromContext(c)
	if publicModel == "" {
		publicModel = originalModel
	}
	// 脱敏：重写 model 字段、删除上游扩展字段、剥离 reasoning 别名。
	// 普通 C 端策略：始终剥离 reasoning，不依赖 account.AgnesChatImageAdapterEnabled()。
	// 即便上游是 DeepSeek-reasoner，客户端也不得看到原始 reasoning。
	// 解析失败时原样返回，避免破坏协议。
	redactedBody := redactChatCompletionsResponse(respBody, publicModel)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Writer.Header().Set("Content-Type", ct)
	} else {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(redactedBody)

	return &OpenAIForwardResult{
		RequestID:                     requestID,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		ReasoningEffort:               reasoningEffort,
		ServiceTier:                   serviceTier,
		Stream:                        false,
		Duration:                      time.Since(startTime),
	}, nil
}

// buildOpenAIChatCompletionsURL 拼接上游 Chat Completions 端点 URL。
//
//   - base 已是 /chat/completions：原样返回
//   - base 以 /v1 结尾：追加 /chat/completions
//   - base 以其他版本段结尾（如 /v4）：追加 /chat/completions
//   - 其他情况：追加 /v1/chat/completions
//
// 与 buildOpenAIResponsesURL 是姐妹函数。
func buildOpenAIChatCompletionsURL(base string) string {
	return buildOpenAIEndpointURL(base, "/v1/chat/completions")
}
