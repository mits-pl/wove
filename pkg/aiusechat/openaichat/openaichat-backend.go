// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package openaichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/launchdarkly/eventsource"
	"github.com/woveterm/wove/pkg/aiusechat/aiutil"
	"github.com/woveterm/wove/pkg/aiusechat/chatstore"
	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/web/sse"
)

// RunChatStep executes a chat step using the chat completions API
func RunChatStep(
	ctx context.Context,
	sseHandler *sse.SSEHandlerCh,
	chatOpts uctypes.WaveChatOpts,
	cont *uctypes.WaveContinueResponse,
) (*uctypes.WaveStopReason, []*StoredChatMessage, *uctypes.RateLimitInfo, error) {
	if sseHandler == nil {
		return nil, nil, nil, errors.New("sse handler is nil")
	}

	chat := chatstore.DefaultChatStore.Get(chatOpts.ChatId)
	if chat == nil {
		return nil, nil, nil, fmt.Errorf("chat not found: %s", chatOpts.ChatId)
	}

	// Save parent context for retries — we apply per-attempt timeout below so
	// each retry gets a FRESH deadline. Previously we wrapped ctx with a single
	// timeout here, then retries reused the already-expired context and failed
	// instantly.
	parentCtx := ctx

	// Convert stored messages to chat completions format
	// Apply context compaction: clear old tool results in API request to save context
	var messages []ChatRequestMessage
	const keepRecentToolResults = 4
	const maxOldToolResultLen = 200

	// Count total tool results
	totalToolResults := 0
	for _, genMsg := range chat.NativeMessages {
		if m, ok := genMsg.(*StoredChatMessage); ok && m.Message.Role == "tool" {
			totalToolResults++
		}
	}

	toolResultSeen := 0
	for _, genMsg := range chat.NativeMessages {
		chatMsg, ok := genMsg.(*StoredChatMessage)
		if !ok {
			return nil, nil, nil, fmt.Errorf("expected StoredChatMessage, got %T", genMsg)
		}
		msg := *chatMsg.Message.clean()

		// Compact old tool results: keep only recent N at full size
		if msg.Role == "tool" {
			toolResultSeen++
			isOld := toolResultSeen <= (totalToolResults - keepRecentToolResults)
			if isOld && len(msg.Content) > maxOldToolResultLen {
				msg.Content = msg.Content[:maxOldToolResultLen] + fmt.Sprintf("\n[...cleared, was %d chars]", len(msg.Content))
			}
		}

		messages = append(messages, msg)
	}

	client, err := aiutil.MakeHTTPClient(chatOpts.Config.ProxyURL)
	if err != nil {
		return nil, nil, nil, err
	}

	// Retry loop with PER-ATTEMPT timeout context.
	// - Each attempt gets a fresh ctx with TimeoutMs deadline so a previous
	//   attempt's expired deadline doesn't kill the retry.
	// - On parent ctx cancellation we abort immediately (no point retrying).
	// - On network/500 errors we retry with backoff (MiniMax drops connections).
	var resp *http.Response
	var lastAttemptCancel context.CancelFunc
	defer func() {
		if lastAttemptCancel != nil {
			// Note: we can't cancel until response body is fully consumed by
			// processChatStream below — but defer here covers the success path.
		}
	}()

	for attempt := 0; attempt < 3; attempt++ {
		// Abort if parent (caller) deadline is gone — retries pointless.
		if parentCtx.Err() != nil {
			return nil, nil, nil, fmt.Errorf("parent context cancelled before request: %w", parentCtx.Err())
		}

		// Build a fresh per-attempt context with its own deadline.
		var attemptCtx context.Context
		var attemptCancel context.CancelFunc
		if chatOpts.Config.TimeoutMs > 0 {
			attemptCtx, attemptCancel = context.WithTimeout(parentCtx, time.Duration(chatOpts.Config.TimeoutMs)*time.Millisecond)
		} else {
			attemptCtx, attemptCancel = context.WithCancel(parentCtx)
		}

		req, err := buildChatHTTPRequest(attemptCtx, messages, chatOpts)
		if err != nil {
			attemptCancel()
			return nil, nil, nil, err
		}
		if attempt == 0 && req.Body != nil && req.ContentLength > 0 {
			log.Printf("[openaichat] request: %d messages, %d tools, body=%dKB\n", len(messages), len(chatOpts.TabTools)+len(chatOpts.Tools)+len(chatOpts.MCPTools), req.ContentLength/1024)
		}

		var attemptErr error
		resp, attemptErr = client.Do(req)
		if attemptErr == nil && resp.StatusCode != http.StatusInternalServerError {
			// Success — keep the attempt context alive for SSE streaming below.
			lastAttemptCancel = attemptCancel
			err = nil
			break
		}

		// Failure on this attempt — release and decide whether to retry.
		if attemptErr == nil && resp.StatusCode == http.StatusInternalServerError {
			resp.Body.Close()
			log.Printf("[openaichat] server error 500 (attempt %d/3), retrying in %ds\n", attempt+1, (attempt+1)*2)
			err = fmt.Errorf("server error 500")
		} else {
			log.Printf("[openaichat] request failed (attempt %d/3), retrying in %ds: %v\n", attempt+1, (attempt+1)*2, attemptErr)
			err = attemptErr
		}
		attemptCancel()

		if attempt < 2 {
			time.Sleep(time.Duration((attempt+1)*2) * time.Second)
		}
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("request failed after 3 retries: %w", err)
	}
	defer resp.Body.Close()
	defer func() {
		if lastAttemptCancel != nil {
			lastAttemptCancel()
		}
	}()
	log.Printf("[openaichat] response status: %d\n", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, nil, nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Setup SSE if this is a new request (not a continuation)
	if cont == nil {
		if err := sseHandler.SetupSSE(); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to setup SSE: %w", err)
		}
	}

	// Stream processing — use parentCtx so the streaming parser doesn't get
	// cut off by the per-attempt deadline (we're already past the request phase).
	stopReason, assistantMsg, err := processChatStream(parentCtx, resp.Body, sseHandler, chatOpts, cont)
	if err != nil {
		return nil, nil, nil, err
	}

	return stopReason, []*StoredChatMessage{assistantMsg}, nil, nil
}

// thinkTagParser tracks state for parsing <think>...</think> tags from a streaming content delta.
// Tags may be split across multiple chunks, so we buffer potential partial tags.
type thinkTagParser struct {
	inThinking       bool            // currently inside <think> block
	tagBuf           strings.Builder // buffering a potential tag boundary
	contentBuilder   strings.Builder // full raw content (with tags) for storage
	sseHandler       *sse.SSEHandlerCh
	textID           string
	reasoningID      string
	textStarted      bool
	reasoningStarted bool
	parseThinkTags   bool // when false, all content is passed through as text (no think tag parsing)
}

func newThinkTagParser(sseHandler *sse.SSEHandlerCh, textID string, reasoningID string, parseThinkTags bool) *thinkTagParser {
	return &thinkTagParser{
		sseHandler:     sseHandler,
		textID:         textID,
		reasoningID:    reasoningID,
		parseThinkTags: parseThinkTags,
	}
}

const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
)

// processChunk handles a content delta chunk, routing text to either reasoning or text SSE events.
// It finds tag boundaries and emits text in batches (not character-by-character) to avoid
// overwhelming the SSE channel buffer.
func (p *thinkTagParser) processChunk(chunk string) {
	p.contentBuilder.WriteString(chunk)

	// When think tag parsing is disabled, pass all content through as text
	if !p.parseThinkTags {
		if !p.textStarted {
			_ = p.sseHandler.AiMsgTextStart(p.textID)
			p.textStarted = true
		}
		_ = p.sseHandler.AiMsgTextDelta(p.textID, chunk)
		return
	}

	// Prepend any previously buffered partial tag content
	if p.tagBuf.Len() > 0 {
		chunk = p.tagBuf.String() + chunk
		p.tagBuf.Reset()
	}

	for len(chunk) > 0 {
		if p.inThinking {
			// Inside <think> block, look for </think>
			idx := strings.Index(chunk, thinkCloseTag)
			if idx >= 0 {
				// Found closing tag - emit reasoning before it, then switch mode
				if idx > 0 {
					p.emitReasoning(chunk[:idx])
				}
				chunk = chunk[idx+len(thinkCloseTag):]
				p.inThinking = false
				if p.reasoningStarted {
					_ = p.sseHandler.AiMsgReasoningEnd(p.reasoningID)
					p.reasoningStarted = false
				}
			} else {
				// No complete closing tag - check if chunk ends with a partial match
				partialLen := p.partialTagMatch(chunk, thinkCloseTag)
				if partialLen > 0 {
					// Emit everything except the potential partial tag
					if len(chunk)-partialLen > 0 {
						p.emitReasoning(chunk[:len(chunk)-partialLen])
					}
					p.tagBuf.WriteString(chunk[len(chunk)-partialLen:])
				} else {
					// No partial match, emit everything as reasoning
					p.emitReasoning(chunk)
				}
				chunk = ""
			}
		} else {
			// Outside <think> block, look for <think>
			idx := strings.Index(chunk, thinkOpenTag)
			if idx >= 0 {
				// Found opening tag - emit text before it, then switch mode
				if idx > 0 {
					p.emitText(chunk[:idx])
				}
				chunk = chunk[idx+len(thinkOpenTag):]
				p.inThinking = true
			} else {
				// No complete opening tag - check if chunk ends with a partial match
				partialLen := p.partialTagMatch(chunk, thinkOpenTag)
				if partialLen > 0 {
					if len(chunk)-partialLen > 0 {
						p.emitText(chunk[:len(chunk)-partialLen])
					}
					p.tagBuf.WriteString(chunk[len(chunk)-partialLen:])
				} else {
					p.emitText(chunk)
				}
				chunk = ""
			}
		}
	}
}

// partialTagMatch checks if the end of s could be the beginning of tag.
// Returns the length of the partial match (0 if none).
func (p *thinkTagParser) partialTagMatch(s string, tag string) int {
	// Check decreasing suffix lengths of s against prefixes of tag
	maxCheck := len(tag) - 1
	if maxCheck > len(s) {
		maxCheck = len(s)
	}
	for i := maxCheck; i >= 1; i-- {
		if strings.HasPrefix(tag, s[len(s)-i:]) {
			return i
		}
	}
	return 0
}

func (p *thinkTagParser) emitText(text string) {
	if text == "" {
		return
	}
	if !p.textStarted {
		_ = p.sseHandler.AiMsgTextStart(p.textID)
		p.textStarted = true
	}
	_ = p.sseHandler.AiMsgTextDelta(p.textID, text)
}

func (p *thinkTagParser) emitReasoning(text string) {
	if text == "" {
		return
	}
	if !p.reasoningStarted {
		_ = p.sseHandler.AiMsgReasoningStart(p.reasoningID)
		p.reasoningStarted = true
	}
	_ = p.sseHandler.AiMsgReasoningDelta(p.reasoningID, text)
}

// flush emits any remaining buffered content (e.g. at end of stream)
func (p *thinkTagParser) flush() {
	if p.tagBuf.Len() == 0 {
		return
	}
	remaining := p.tagBuf.String()
	p.tagBuf.Reset()
	if p.inThinking {
		if !p.reasoningStarted {
			_ = p.sseHandler.AiMsgReasoningStart(p.reasoningID)
			p.reasoningStarted = true
		}
		_ = p.sseHandler.AiMsgReasoningDelta(p.reasoningID, remaining)
	} else {
		if !p.textStarted {
			_ = p.sseHandler.AiMsgTextStart(p.textID)
			p.textStarted = true
		}
		_ = p.sseHandler.AiMsgTextDelta(p.textID, remaining)
	}
}

// finalize closes any open reasoning/text blocks
func (p *thinkTagParser) finalize() {
	p.flush()
	if p.reasoningStarted {
		_ = p.sseHandler.AiMsgReasoningEnd(p.reasoningID)
		p.reasoningStarted = false
	}
	if p.textStarted {
		_ = p.sseHandler.AiMsgTextEnd(p.textID)
		p.textStarted = false
	}
}

// modelUsesThinkTags returns true for models known to embed reasoning in <think>...</think> tags
// within the content field (rather than using a dedicated reasoning_content API field).
//
// NOTE: MiniMax is intentionally NOT in this list anymore. With reasoning_split=true
// (which we enable for all minimax models in convertmessage.go), MiniMax returns
// reasoning in the dedicated `reasoning_details` field, NOT in <think> tags.
// We parse reasoning_details in processChatStream and pass them back via the
// ReasoningDetails field on next turn (Mini-Agent compatible).
func modelUsesThinkTags(model string) bool {
	m := strings.ToLower(model)
	thinkTagModels := []string{"deepseek", "qwen", "glm", "yi-"}
	for _, prefix := range thinkTagModels {
		if strings.Contains(m, prefix) {
			return true
		}
	}
	return false
}

// StripThinkTagsFromHistory removes <think>...</think> from stored messages.
// Set to true for bench runs to prevent context bloat from MiniMax/DeepSeek reasoning.
var StripThinkTagsFromHistory bool

func stripThinkTags(s string) string {
	for {
		start := strings.Index(s, thinkOpenTag)
		if start == -1 {
			return s
		}
		end := strings.Index(s[start:], thinkCloseTag)
		if end == -1 {
			return s[:start] // unclosed tag, strip from tag to end
		}
		s = s[:start] + s[start+end+len(thinkCloseTag):]
	}
}

func processChatStream(
	ctx context.Context,
	body io.Reader,
	sseHandler *sse.SSEHandlerCh,
	chatOpts uctypes.WaveChatOpts,
	cont *uctypes.WaveContinueResponse,
) (*uctypes.WaveStopReason, *StoredChatMessage, error) {
	decoder := eventsource.NewDecoder(body)
	msgID := uuid.New().String()
	textID := uuid.New().String()
	reasoningID := uuid.New().String()
	var finishReason string
	var toolCallsInProgress []ToolCall
	var streamUsage *ChatUsage

	parser := newThinkTagParser(sseHandler, textID, reasoningID, modelUsesThinkTags(chatOpts.Config.Model))

	// Accumulator for MiniMax reasoning_details (when reasoning_split=true).
	// Stream chunks include delta.reasoning_details[]; we accumulate text per
	// (id, index) so we can replay the full reasoning back to the model on the
	// next turn. CRITICAL for Interleaved Thinking.
	type rdAccum struct {
		key       string // id+index
		typ       string
		id        string
		format    string
		idx       int
		text      strings.Builder
		seenOrder int // first-seen position to preserve order
	}
	var rdList []*rdAccum
	rdByKey := map[string]*rdAccum{}

	if cont == nil {
		_ = sseHandler.AiMsgStart(msgID)
	}
	_ = sseHandler.AiMsgStartStep()

	lastChunkTime := time.Now()
	chunkCount := 0
	for {
		if err := ctx.Err(); err != nil {
			log.Printf("[openaichat] context cancelled after %d chunks, last chunk %dms ago\n", chunkCount, time.Since(lastChunkTime).Milliseconds())
			_ = sseHandler.AiMsgError("request cancelled")
			return &uctypes.WaveStopReason{
				Kind:      uctypes.StopKindCanceled,
				ErrorType: "cancelled",
				ErrorText: "request cancelled",
			}, nil, err
		}

		event, err := decoder.Decode()
		chunkCount++
		silenceMs := time.Since(lastChunkTime).Milliseconds()
		lastChunkTime = time.Now()
		if err != nil {
			if errors.Is(err, io.EOF) {
				log.Printf("[openaichat] stream EOF after %d chunks (silence: %dms)\n", chunkCount, silenceMs)
				break
			}
			if sseHandler.Err() != nil {
				partialMsg := extractPartialTextMessage(msgID, parser.contentBuilder.String())
				return &uctypes.WaveStopReason{
					Kind:      uctypes.StopKindCanceled,
					ErrorType: "client_disconnect",
					ErrorText: "client disconnected",
				}, partialMsg, nil
			}
			_ = sseHandler.AiMsgError(err.Error())
			return &uctypes.WaveStopReason{
				Kind:      uctypes.StopKindError,
				ErrorType: "stream",
				ErrorText: err.Error(),
			}, nil, fmt.Errorf("stream decode error: %w", err)
		}

		data := event.Data()
		if data == "[DONE]" {
			log.Printf("[openaichat] stream [DONE] after %d chunks (silence: %dms)\n", chunkCount, silenceMs)
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Printf("openaichat: failed to parse chunk: %v\n", err)
			continue
		}

		// Capture usage from the final chunk (sent with stream_options.include_usage)
		if chunk.Usage != nil {
			streamUsage = chunk.Usage
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			parser.processChunk(choice.Delta.Content)
		}

		// MiniMax Interleaved Thinking: accumulate reasoning_details deltas.
		// Each chunk may include incremental text for one or more reasoning blocks.
		// We also forward the deltas to the SSE handler as "reasoning" events so
		// the UI can display the chain of thought live.
		for _, rd := range choice.Delta.ReasoningDetails {
			key := fmt.Sprintf("%s/%d", rd.ID, rd.Index)
			acc, ok := rdByKey[key]
			if !ok {
				acc = &rdAccum{
					key:       key,
					typ:       rd.Type,
					id:        rd.ID,
					format:    rd.Format,
					idx:       rd.Index,
					seenOrder: len(rdList),
				}
				rdByKey[key] = acc
				rdList = append(rdList, acc)
			}
			if rd.Text != "" {
				acc.text.WriteString(rd.Text)
				// Stream to SSE as reasoning so the UI sees live thinking.
				if !parser.reasoningStarted {
					_ = parser.sseHandler.AiMsgReasoningStart(parser.reasoningID)
					parser.reasoningStarted = true
				}
				_ = parser.sseHandler.AiMsgReasoningDelta(parser.reasoningID, rd.Text)
			}
		}

		if len(choice.Delta.ToolCalls) > 0 {
			for _, tcDelta := range choice.Delta.ToolCalls {
				idx := tcDelta.Index
				for len(toolCallsInProgress) <= idx {
					toolCallsInProgress = append(toolCallsInProgress, ToolCall{Type: "function"})
				}

				tc := &toolCallsInProgress[idx]
				if tcDelta.ID != "" {
					tc.ID = tcDelta.ID
				}
				if tcDelta.Type != "" {
					tc.Type = tcDelta.Type
				}
				if tcDelta.Function != nil {
					if tcDelta.Function.Name != "" {
						tc.Function.Name = tcDelta.Function.Name
					}
					if tcDelta.Function.Arguments != "" {
						tc.Function.Arguments += tcDelta.Function.Arguments
					}
				}
			}
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" {
			finishReason = *choice.FinishReason
			log.Printf("[openaichat] stream finish_reason=%q at chunk %d\n", finishReason, chunkCount)
		}
	}
	log.Printf("[openaichat] stream ended: %d chunks, finish_reason=%q, tool_calls=%d, content_len=%d\n",
		chunkCount, finishReason, len(toolCallsInProgress), parser.contentBuilder.Len())

	stopKind := uctypes.StopKindDone
	if finishReason == "length" {
		stopKind = uctypes.StopKindMaxTokens
	} else if finishReason == "tool_calls" {
		stopKind = uctypes.StopKindToolUse
	}

	var validToolCalls []ToolCall
	for _, tc := range toolCallsInProgress {
		if tc.ID != "" && tc.Function.Name != "" {
			validToolCalls = append(validToolCalls, tc)
		}
	}

	var waveToolCalls []uctypes.WaveToolCall
	if len(validToolCalls) > 0 {
		for _, tc := range validToolCalls {
			var inputJSON any
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &inputJSON); err != nil {
					log.Printf("openaichat: failed to parse tool call arguments: %v\n", err)
					continue
				}
			}
			waveToolCalls = append(waveToolCalls, uctypes.WaveToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: inputJSON,
			})
		}
	}

	stopReason := &uctypes.WaveStopReason{
		Kind:      stopKind,
		RawReason: finishReason,
		ToolCalls: waveToolCalls,
	}

	// Finalize parser: close any open reasoning/text SSE blocks
	parser.finalize()

	// Build the final reasoning_details list to send back on the next turn.
	// MiniMax requires this for Interleaved Thinking — without it, the model
	// loses its chain of thought between turns.
	var assistantReasoning []ReasoningDetail
	if len(rdList) > 0 {
		// Sort by first-seen order (slice is already in that order).
		assistantReasoning = make([]ReasoningDetail, 0, len(rdList))
		for _, acc := range rdList {
			assistantReasoning = append(assistantReasoning, ReasoningDetail{
				Type:   acc.typ,
				ID:     acc.id,
				Format: acc.format,
				Index:  acc.idx,
				Text:   acc.text.String(),
			})
		}
		log.Printf("[openaichat] captured %d reasoning_details block(s) from stream\n", len(assistantReasoning))
	}

	assistantMsg := &StoredChatMessage{
		MessageId: msgID,
		Message: ChatRequestMessage{
			Role:             "assistant",
			ReasoningDetails: assistantReasoning,
		},
		Usage: streamUsage,
	}

	// Store content for conversation history.
	// Strip <think> tags to prevent context bloat on models that embed reasoning
	// (MiniMax, DeepSeek). Model generates fresh thinking each turn anyway.
	content := parser.contentBuilder.String()
	if StripThinkTagsFromHistory && parser.parseThinkTags {
		content = stripThinkTags(content)
	}
	if len(validToolCalls) > 0 {
		assistantMsg.Message.ToolCalls = validToolCalls
		if len(content) > 0 {
			assistantMsg.Message.Content = content
		}
	} else {
		assistantMsg.Message.Content = content
	}

	// Send usage data to frontend
	if streamUsage != nil {
		_ = sseHandler.AiMsgData("data-usage", msgID, map[string]any{
			"input_tokens":  streamUsage.InputTokens,
			"output_tokens": streamUsage.OutputTokens,
			"total_tokens":  streamUsage.TotalTokens,
		})
	}

	_ = sseHandler.AiMsgFinishStep()
	if stopKind != uctypes.StopKindToolUse {
		_ = sseHandler.AiMsgFinish(finishReason, nil)
	}

	return stopReason, assistantMsg, nil
}

func extractPartialTextMessage(msgID string, text string) *StoredChatMessage {
	if text == "" {
		return nil
	}

	return &StoredChatMessage{
		MessageId: msgID,
		Message: ChatRequestMessage{
			Role:    "assistant",
			Content: text,
		},
	}
}
