package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ForwardAsChatCompletions accepts an OpenAI Chat Completions API request body,
// converts it to Anthropic Messages format (chained via Responses format),
// forwards to the Anthropic upstream, and converts the response back to Chat
// Completions format. This enables Chat Completions clients to access Anthropic
// models through Anthropic platform groups.
func (s *GatewayService) ForwardAsChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	parsed *ParsedRequest,
) (*ForwardResult, error) {
	startTime := time.Now()

	// 1. Parse Chat Completions request
	var ccReq apicompat.ChatCompletionsRequest
	if err := json.Unmarshal(body, &ccReq); err != nil {
		return nil, fmt.Errorf("parse chat completions request: %w", err)
	}
	originalModel := ccReq.Model
	clientStream := ccReq.Stream
	includeUsage := ccReq.StreamOptions != nil && ccReq.StreamOptions.IncludeUsage

	// 1b. 解析 publicModel 用于身份提示词注入和客户端响应脱敏。
	// publicModel 必须来自服务端解析后的 ResolvedModel.PublicModel（由 handler 注入 gin.Context）。
	// 未注入时 fallback 到 originalModel（保持向后兼容），但**不注入**身份提示词，
	// 避免破坏未配置渠道映射场景下的 messages 顺序（如 DeepSeek reasoning_content 回放）。
	publicModelFromContext := getPublicModelFromContext(c)
	publicModel := publicModelFromContext
	if publicModel == "" {
		publicModel = originalModel
	}

	// 1c. 注入内部身份提示词（隐藏真实上游模型），位于客户端消息之前。
	// 必须在 CC→Responses 转换之前注入，使身份提示词成为 messages[0]，
	// 后续 ChatCompletionsToResponses 会自然保留这条消息。
	// 不修改客户端原始 body，仅修改函数内 ccReq 副本。
	//
	// 仅当 handler 显式注入了 PublicModel（即配置了渠道映射）时才注入。
	// 仅对 APIKey/ServiceAccount 账号注入：这些账号可能对接第三方 OpenAI 兼容上游，
	// 需要隐藏真实上游身份；OAuth 账号上游即原生 Anthropic，无身份泄露风险，
	// 且 OAuth 路径会把 system 消息折叠为 instructions 字段，注入会破坏既有契约。
	if publicModelFromContext != "" && account.Type != AccountTypeOAuth {
		if contentBytes, mErr := json.Marshal(buildIdentitySystemPrompt(publicModelFromContext)); mErr == nil {
			identityMsg := apicompat.ChatMessage{
				Role:    "system",
				Content: contentBytes,
			}
			ccReq.Messages = append([]apicompat.ChatMessage{identityMsg}, ccReq.Messages...)
		}
	}

	// 2. Convert CC → Responses → Anthropic (chained conversion)
	responsesReq, err := apicompat.ChatCompletionsToResponses(&ccReq)
	if err != nil {
		return nil, fmt.Errorf("convert chat completions to responses: %w", err)
	}

	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	// 3. Force upstream streaming
	anthropicReq.Stream = true
	reqStream := true

	// 4. Model mapping
	mappedModel := originalModel
	if account.Type == AccountTypeAPIKey || account.Type == AccountTypeServiceAccount {
		mappedModel = account.GetMappedModel(originalModel)
	}
	if mappedModel == originalModel && account.Platform == PlatformAnthropic && account.Type == AccountTypeServiceAccount {
		normalized := normalizeVertexAnthropicModelID(claude.NormalizeModelID(originalModel))
		if normalized != originalModel {
			mappedModel = normalized
		}
	} else if mappedModel == originalModel && account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
		normalized := claude.NormalizeModelID(originalModel)
		if normalized != originalModel {
			mappedModel = normalized
		}
	}
	anthropicReq.Model = mappedModel

	logger.L().Debug("gateway forward_as_chat_completions: model mapping applied",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("mapped_model", mappedModel),
		zap.Bool("client_stream", clientStream),
	)

	// 5. Marshal Anthropic request body
	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// 6. Apply Claude Code mimicry for OAuth accounts.
	// Chat Completions 协议进来的请求永远不是 Claude Code 客户端，所以对 OAuth 账号
	// 必须完整执行 /v1/messages 主路径上的伪装链路（system 重写 + normalize + metadata 注入），
	// 否则会被 Anthropic 判为第三方应用并扣 extra usage。
	// 见 applyClaudeCodeOAuthMimicryToBody 的 godoc。
	isClaudeCode := false
	shouldMimicClaudeCode := account.IsOAuth() && !isClaudeCode

	if shouldMimicClaudeCode {
		anthropicBody = s.applyClaudeCodeOAuthMimicryToBody(ctx, c, account, anthropicBody, anthropicReq.System, mappedModel)
	}

	// 7. Enforce cache_control block limit
	anthropicBody = enforceCacheControlLimit(anthropicBody)

	// 8. Get access token
	token, tokenType, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	// 9. Get proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// 10. Build upstream request
	upstreamCtx, releaseUpstreamCtx := detachStreamUpstreamContext(ctx, reqStream)
	upstreamReq, _, err := s.buildUpstreamRequest(upstreamCtx, c, account, anthropicBody, token, tokenType, mappedModel, reqStream, shouldMimicClaudeCode)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	// 11. Send request
	resp, err := s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		writeGatewayCCError(c, http.StatusBadGateway, "server_error", "Upstream request failed")
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	// 12. Handle error response with failover
	if resp.StatusCode >= 400 {
		respBody, _ := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))

		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		// 额外脱敏：把客户端可见错误消息中出现的真实上游模型名替换为 "model"。
		upstreamMsg = redactUpstreamModelInMessage(upstreamMsg, getUpstreamModelFromContext(c))

		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
			})
			if s.rateLimitService != nil {
				s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, mappedModel)
			}
			return nil, &UpstreamFailoverError{
				StatusCode:   resp.StatusCode,
				ResponseBody: respBody,
			}
		}

		writeGatewayCCError(c, mapUpstreamStatusCode(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}

	// 13. Extract reasoning effort from CC request body
	reasoningEffort := extractCCReasoningEffortFromBody(body)
	// 国产模型默认 effort 补充：本路径是客户端 CC 请求 → Anthropic 上游，
	// 如果上游是 passback-required 国产模型 (Kimi-anthropic / GLM-anthropic / MiniMax)
	// 且客户端在 body 里传了 thinking.type=enabled，补中默认 effort。
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, mappedModel)

	// 14. Handle normal response
	// Read Anthropic SSE → convert to Responses events → convert to CC format
	var result *ForwardResult
	var handleErr error
	if clientStream {
		result, handleErr = s.handleCCStreamingFromAnthropic(resp, c, originalModel, publicModel, mappedModel, reasoningEffort, startTime, includeUsage)
	} else {
		result, handleErr = s.handleCCBufferedFromAnthropic(resp, c, originalModel, publicModel, mappedModel, reasoningEffort, startTime)
	}

	return result, handleErr
}

// extractCCReasoningEffortFromBody reads reasoning effort from a Chat Completions
// request body. It checks both nested (reasoning.effort) and flat (reasoning_effort)
// formats used by OpenAI-compatible clients.
func extractCCReasoningEffortFromBody(body []byte) *string {
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	}
	if raw == "" {
		return nil
	}
	normalized := normalizeOpenAIReasoningEffort(raw)
	if normalized == "" {
		return nil
	}
	return &normalized
}

// handleCCBufferedFromAnthropic reads Anthropic SSE events, assembles the full
// response, then converts Anthropic → Responses → Chat Completions.
//
// publicModel 用于客户端响应的 model 字段（隐藏真实上游模型）。
// 必须来自服务端解析后的 ResolvedModel.PublicModel，不能使用 mappedModel 或 originalModel
// （后者在 Anthropic CC 路径下可能是渠道映射后的模型名）。
func (s *GatewayService) handleCCBufferedFromAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	publicModel string,
	mappedModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*ForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	var finalResp *apicompat.AnthropicResponse
	var usage ClaudeUsage

	for scanner.Scan() {
		line := scanner.Text()
		// SSE 规范允许 `event:xxx`（冒号后无空格）：Kimi 等 Anthropic 兼容上游
		// 返回紧凑格式，严格匹配 "event: " 会丢弃全部事件（#4653 同根因）。
		if _, ok := extractOpenAISSEEventLine(line); !ok {
			continue
		}

		if !scanner.Scan() {
			break
		}
		payload, ok := extractOpenAISSEDataLine(scanner.Text())
		if !ok {
			continue
		}

		var event apicompat.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		// message_start carries the initial response structure and cache usage
		if event.Type == "message_start" && event.Message != nil {
			finalResp = event.Message
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}

		// message_delta carries final usage and stop_reason
		if event.Type == "message_delta" {
			if event.Usage != nil {
				mergeAnthropicUsage(&usage, *event.Usage)
			}
			if event.Delta != nil && event.Delta.StopReason != "" && finalResp != nil {
				finalResp.StopReason = apicompat.AnthropicStopReasonPtr(event.Delta.StopReason)
			}
		}
		if event.Type == "content_block_start" && event.ContentBlock != nil && finalResp != nil {
			finalResp.Content = append(finalResp.Content, *event.ContentBlock)
		}
		if event.Type == "content_block_delta" && event.Delta != nil && finalResp != nil && event.Index != nil {
			idx := *event.Index
			if idx < len(finalResp.Content) {
				switch event.Delta.Type {
				case "text_delta":
					finalResp.Content[idx].Text += event.Delta.Text
				case "thinking_delta":
					finalResp.Content[idx].Thinking += event.Delta.Thinking
				case "input_json_delta":
					finalResp.Content[idx].Input = appendRawJSON(finalResp.Content[idx].Input, event.Delta.PartialJSON)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("forward_as_cc buffered: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}

	if finalResp == nil {
		writeGatewayCCError(c, http.StatusBadGateway, "server_error", "Upstream stream ended without a response")
		return nil, fmt.Errorf("upstream stream ended without response")
	}

	// Update usage from accumulated delta
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		finalResp.Usage = apicompat.AnthropicUsage{
			InputTokens:              usage.InputTokens,
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
		}
	}

	// Chain: Anthropic → Responses → Chat Completions
	responsesResp := apicompat.AnthropicToResponsesResponse(finalResp)
	// 使用 publicModel 作为客户端响应的 model 字段，隐藏真实上游模型。
	ccResp := apicompat.ResponsesToChatCompletions(responsesResp, publicModel)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	// 非流式响应必须是 application/json。上游被强制流式后会返回
	// Content-Type: text/event-stream，经 WriteFilteredHeaders 透传后会污染
	// 响应头；而 c.Data/c.JSON 走 Gin 的 writeContentType（仅当头不存在时才设置），
	// 无法覆盖已存在的 SSE 头。这里显式 Set 强制改回 JSON，避免下游中间层
	// （如 new-api）按 Content-Type 误判为流式。
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Marshal then bytes-replace so tool name mapping is reversed at byte level
	// (parity with Parrot non-stream flow that marshals → restore → emit).
	if respBytes, err := json.Marshal(ccResp); err == nil {
		respBytes = reverseToolNamesIfPresent(c, respBytes)
		// 平台级 C 端策略：剥离 reasoning_content 及别名、重写 model、删除上游 metadata。
		// Anthropic thinking content block 会被 AnthropicToResponsesResponse 转换为
		// Responses reasoning output，再由 ResponsesToChatCompletions 映射为 CC
		// message.reasoning_content。此处必须脱敏，否则 Anthropic thinking 会泄露给
		// 普通 OpenAI 兼容客户端。
		respBytes = redactChatCompletionsResponse(respBytes, publicModel)
		c.Data(http.StatusOK, "application/json; charset=utf-8", respBytes)
	} else {
		c.JSON(http.StatusOK, ccResp)
	}

	return &ForwardResult{
		RequestID:       requestID,
		Usage:           usage,
		Model:           publicModel,
		UpstreamModel:   mappedModel,
		ReasoningEffort: reasoningEffort,
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

// handleCCStreamingFromAnthropic reads Anthropic SSE events, converts each
// to Responses events, then to Chat Completions chunks, and writes them.
//
// publicModel 用于客户端响应的 model 字段（隐藏真实上游模型）。
// 必须来自服务端解析后的 ResolvedModel.PublicModel，不能使用 mappedModel 或 originalModel
// （后者在 Anthropic CC 路径下可能是渠道映射后的模型名）。
func (s *GatewayService) handleCCStreamingFromAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	publicModel string,
	mappedModel string,
	reasoningEffort *string,
	startTime time.Time,
	includeUsage bool,
) (*ForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	// Use Anthropic→Responses state machine, then convert Responses→CC
	// 使用 publicModel 作为客户端响应的 model 字段，隐藏真实上游模型。
	anthState := apicompat.NewAnthropicEventToResponsesState()
	anthState.Model = publicModel
	ccState := apicompat.NewResponsesEventToChatState()
	ccState.Model = publicModel
	ccState.IncludeUsage = includeUsage

	var usage ClaudeUsage
	var firstTokenMs *int
	firstChunk := true

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	resultWithUsage := func() *ForwardResult {
		return &ForwardResult{
			RequestID:       requestID,
			Usage:           usage,
			Model:           publicModel,
			UpstreamModel:   mappedModel,
			ReasoningEffort: reasoningEffort,
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    firstTokenMs,
		}
	}

	writeChunk := func(chunk apicompat.ChatCompletionsChunk) bool {
		data, err := json.Marshal(chunk)
		if err != nil {
			return false
		}
		// Reverse tool name mapping: fake → real, per-chunk bytes.Replace.
		// c 可能持有请求侧注入的 ToolNameRewrite；无则仅做静态前缀还原。
		// 必须在 redactChatCompletionsStreamChunk 之前执行：reverseToolNamesIfPresent
		// 在字节级做替换，需要完整的 JSON 上下文；脱敏后的 JSON 可能因字段删除改变
		// 字节边界，导致替换错位。
		payload := string(reverseToolNamesIfPresent(c, data))
		// 平台级 C 端策略：剥离 reasoning_content 及别名、重写 model、删除上游 metadata。
		// Anthropic thinking content block 会被 AnthropicEventToResponsesEvents 转换为
		// Responses reasoning delta，再由 ResponsesEventToChatChunks 映射为 CC
		// chunk.choices[].delta.reasoning_content。此处必须脱敏，否则 Anthropic
		// thinking 会通过 SSE 流式泄露给普通 OpenAI 兼容客户端。
		redactedPayload, result := redactChatCompletionsStreamChunk(payload, publicModel)
		switch result {
		case ChunkFatal:
			// Fail-closed：malformed JSON 不得透传原文。本路径的 JSON 由 json.Marshal
			// 构造，理论不应失败；若仍失败说明发生了内存损坏或并发修改。
			// 记录不含原文的警告日志并丢弃该 chunk。
			logger.L().Warn("forward_as_cc stream: malformed SSE JSON after construction",
				zap.String("request_id", requestID),
			)
			return false
		case ChunkDrop:
			// reasoning-only chunk 删除后无有效载荷，丢弃。
			return false
		case ChunkPass:
			// 正常写出（payload 可能已被脱敏）。
		}
		if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", redactedPayload); err != nil {
			return true // client disconnected
		}
		return false
	}

	processAnthropicEvent := func(event *apicompat.AnthropicStreamEvent) bool {
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		// Extract usage from message_delta
		if event.Type == "message_delta" && event.Usage != nil {
			mergeAnthropicUsage(&usage, *event.Usage)
		}
		// Also capture usage from message_start (carries cache fields)
		if event.Type == "message_start" && event.Message != nil {
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}

		// Chain: Anthropic event → Responses events → CC chunks
		responsesEvents := apicompat.AnthropicEventToResponsesEvents(event, anthState)
		for _, resEvt := range responsesEvents {
			ccChunks := apicompat.ResponsesEventToChatChunks(&resEvt, ccState)
			for _, chunk := range ccChunks {
				if disconnected := writeChunk(chunk); disconnected {
					return true
				}
			}
		}
		c.Writer.Flush()
		return false
	}

	for scanner.Scan() {
		line := scanner.Text()
		// 与缓冲路径一致：接受 SSE 紧凑格式（冒号后无空格，#4653 同根因）。
		if _, ok := extractOpenAISSEEventLine(line); !ok {
			continue
		}

		if !scanner.Scan() {
			break
		}
		payload, ok := extractOpenAISSEDataLine(scanner.Text())
		if !ok {
			continue
		}

		var event apicompat.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		if processAnthropicEvent(&event) {
			return resultWithUsage(), nil
		}
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("forward_as_cc stream: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}

	// Finalize both state machines
	finalResEvents := apicompat.FinalizeAnthropicResponsesStream(anthState)
	for _, resEvt := range finalResEvents {
		ccChunks := apicompat.ResponsesEventToChatChunks(&resEvt, ccState)
		for _, chunk := range ccChunks {
			writeChunk(chunk) //nolint:errcheck
		}
	}
	finalCCChunks := apicompat.FinalizeResponsesChatStream(ccState)
	for _, chunk := range finalCCChunks {
		writeChunk(chunk) //nolint:errcheck
	}

	// Write [DONE] marker
	fmt.Fprint(c.Writer, "data: [DONE]\n\n") //nolint:errcheck
	c.Writer.Flush()

	return resultWithUsage(), nil
}

// writeGatewayCCError writes an error in OpenAI Chat Completions format for
// the Anthropic-upstream CC forwarding path.
func writeGatewayCCError(c *gin.Context, statusCode int, errType, message string) {
	MarkResponseCommitted(c)
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}
