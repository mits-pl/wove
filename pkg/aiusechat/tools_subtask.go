// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/woveterm/wove/pkg/aiusechat/chatstore"
	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/waveobj"
	"github.com/woveterm/wove/pkg/wcore"
	"github.com/woveterm/wove/pkg/web/sse"
	"github.com/woveterm/wove/pkg/wps"
	"github.com/woveterm/wove/pkg/wstore"
)

const (
	subTaskMaxDepth   = 2
	subTaskTimeout    = 5 * time.Minute
	subTaskSummaryLen = 1500
)

// publishSubTaskEvent sends a sub-task status update via wps for the given tab scope.
func publishSubTaskEvent(tabId string, data *wps.SubTaskUpdateData) {
	if tabId == "" {
		return
	}
	tabORef := waveobj.MakeORef(waveobj.OType_Tab, tabId)
	wps.Broker.Publish(wps.WaveEvent{
		Event:  wps.Event_SubTaskUpdate,
		Scopes: []string{tabORef.String()},
		Data:   data,
	})
}

// GetRunSubTaskToolDefinition returns a tool that spawns an isolated AI sub-task in a new tab.
func GetRunSubTaskToolDefinition(parentOpts *uctypes.WaveChatOpts) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "run_sub_task",
		DisplayName:      "Run Sub-Task",
		Description:      "Execute an AI sub-task in a new tab with isolated conversation context. Use this for multi-step tasks where accumulated tool results would overflow the context window. The sub-task has access to the same tools (terminal, web browser, etc.). Results are saved to a file; only a summary is returned here.",
		ShortDescription: "Run isolated AI sub-task in new tab",
		ToolLogName:      "gen:subtask",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "Detailed description of the task to execute. Be specific — the sub-task AI has no context from this conversation.",
				},
				"output_file": map[string]any{
					"type":        "string",
					"description": "Absolute file path to save the full results (e.g., /tmp/seo/01-technical.md). Parent directories will be created if needed.",
				},
			},
			"required": []string{"task", "output_file"},
		},
		ToolTextCallback: makeSubTaskCallback(parentOpts),
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			task, _ := inputMap["task"].(string)
			if len(task) > 60 {
				task = task[:57] + "..."
			}
			if output != nil {
				return fmt.Sprintf("sub-task completed: %s", task)
			}
			return fmt.Sprintf("running sub-task: %s", task)
		},
	}
}

func makeSubTaskCallback(parentOpts *uctypes.WaveChatOpts) func(any) (string, error) {
	return func(input any) (string, error) {
		inputMap, ok := input.(map[string]any)
		if !ok {
			return "", fmt.Errorf("invalid input")
		}
		task, _ := inputMap["task"].(string)
		if task == "" {
			return "", fmt.Errorf("task is required")
		}
		outputFile, _ := inputMap["output_file"].(string)

		// Depth check
		if parentOpts.SubTaskDepth >= subTaskMaxDepth {
			return "", fmt.Errorf("sub-task nesting limit (%d) reached", subTaskMaxDepth)
		}

		return RunSubTaskChat(context.Background(), parentOpts, task, outputFile)
	}
}

// RunSubTaskChat creates a new tab and runs an isolated AI conversation in it.
func RunSubTaskChat(ctx context.Context, parentOpts *uctypes.WaveChatOpts, taskPrompt string, outputFile string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, subTaskTimeout)
	defer cancel()

	subChatId := uuid.New().String()

	// Create a task preview for the tab name
	taskPreview := taskPrompt
	if len(taskPreview) > 40 {
		taskPreview = taskPreview[:37] + "..."
	}

	// Find workspace for the parent tab
	workspaceId, err := findWorkspaceForTab(ctx, parentOpts.TabId)
	if err != nil {
		log.Printf("[subtask] could not find workspace for tab %s: %v, running without tab\n", parentOpts.TabId, err)
	}

	var subTabId string
	if workspaceId != "" {
		// Create a new tab (don't activate — user stays on parent tab)
		tabCtx := waveobj.ContextWithUpdates(ctx)
		newTabId, tabErr := wcore.CreateTab(tabCtx, workspaceId, "Sub: "+taskPreview, false, false)
		if tabErr != nil {
			log.Printf("[subtask] failed to create tab: %v, running without tab\n", tabErr)
		} else {
			subTabId = newTabId
			// Set the chatId and sub-task metadata on the new tab's RTInfo
			tabORef := waveobj.MakeORef(waveobj.OType_Tab, subTabId)
			wstore.SetRTInfo(tabORef, map[string]any{
				"waveai:chatid":         subChatId,
				"waveai:subtask-status": "running",
				"waveai:subtask-parent": parentOpts.ChatId,
			})
			// Broadcast updates so frontend sees the new tab
			updates := waveobj.ContextGetUpdatesRtn(tabCtx)
			wps.Broker.SendUpdateEvents(updates)
			// Publish running event
			publishSubTaskEvent(subTabId, &wps.SubTaskUpdateData{
				ChatId:   subChatId,
				ParentId: parentOpts.ChatId,
				Status:   "running",
			})
		}
	}

	// Build sub-task WaveChatOpts with its own widget ownership tracker
	subOwnedWidgets := uctypes.NewOwnedWidgetSet()
	subOpts := uctypes.WaveChatOpts{
		ChatId:           subChatId,
		ClientId:         parentOpts.ClientId,
		Config:           parentOpts.Config,
		WidgetAccess:     parentOpts.WidgetAccess,
		MCPAccess:        parentOpts.MCPAccess,
		AutoApproveTools: true,
		SubTaskDepth:     parentOpts.SubTaskDepth + 1,
		OwnedWidgets:     subOwnedWidgets,
		SystemPrompt: []string{
			"You are executing a focused sub-task. Complete the task thoroughly and provide a comprehensive result.",
			"Do not ask clarifying questions — use your best judgment.",
			"All tools mentioned (web_open, web_read_text, web_seo_audit, web_exec_js, term_run_command, etc.) are AI tool calls you invoke directly — NOT shell commands.",
		},
	}

	// Always use the parent tab for widget creation/interaction so browsers and
	// terminals opened by the subtask are visible to the user in the active tab.
	// We must create a NEW TabStateGenerator that passes &subOpts (with the sub-task's
	// OwnedWidgets) instead of reusing the parent's generator, otherwise widgets opened
	// by the sub-task get registered in the parent's OwnedWidgets and won't be cleaned up.
	if parentOpts.TabId != "" {
		capturedTabId := parentOpts.TabId
		subOpts.TabStateGenerator = func() (string, []uctypes.ToolDefinition, string, error) {
			tabState, tabTools, err := GenerateTabStateAndTools(ctx, capturedTabId, parentOpts.WidgetAccess, &subOpts)
			return tabState, tabTools, capturedTabId, err
		}
	} else if subTabId != "" {
		capturedTabId := subTabId
		subOpts.TabStateGenerator = func() (string, []uctypes.ToolDefinition, string, error) {
			tabState, tabTools, err := GenerateTabStateAndTools(ctx, capturedTabId, parentOpts.WidgetAccess, &subOpts)
			return tabState, tabTools, capturedTabId, err
		}
	}

	// Create SSE handler with httptest.ResponseRecorder (output goes to buffer)
	recorder := httptest.NewRecorder()
	sseHandler := sse.MakeSSEHandlerCh(recorder, ctx)
	defer sseHandler.Close()

	// Get backend
	backend, err := GetBackendByAPIType(subOpts.Config.APIType)
	if err != nil {
		return "", fmt.Errorf("failed to get backend: %w", err)
	}

	// Post the task prompt as a user message
	userMsg := uctypes.AIMessage{
		MessageId: uuid.New().String(),
		Parts: []uctypes.AIMessagePart{
			{Type: uctypes.AIMessagePartTypeText, Text: taskPrompt},
		},
	}
	nativeMsg, err := backend.ConvertAIMessageToNativeChatMessage(userMsg)
	if err != nil {
		return "", fmt.Errorf("failed to convert message: %w", err)
	}
	if err := chatstore.DefaultChatStore.PostMessage(subChatId, &subOpts.Config, nativeMsg); err != nil {
		return "", fmt.Errorf("failed to store message: %w", err)
	}

	// Run the AI conversation
	log.Printf("[subtask] starting sub-task chat=%s tab=%s depth=%d\n", subChatId, subTabId, subOpts.SubTaskDepth)
	_, err = RunAIChat(ctx, sseHandler, backend, subOpts)
	if err != nil {
		log.Printf("[subtask] chat error: %v\n", err)
	}

	// Auto-cleanup: close all widgets that this subtask opened
	CleanupOwnedWidgets(subOwnedWidgets)

	// Extract the final assistant text from chatstore
	resultText := extractLastAssistantText(backend, subChatId)
	if resultText == "" && err != nil {
		// Publish error status
		if subTabId != "" {
			tabORef := waveobj.MakeORef(waveobj.OType_Tab, subTabId)
			wstore.SetRTInfo(tabORef, map[string]any{"waveai:subtask-status": "error"})
			publishSubTaskEvent(subTabId, &wps.SubTaskUpdateData{
				ChatId:   subChatId,
				ParentId: parentOpts.ChatId,
				Status:   "error",
				Summary:  err.Error(),
			})
		}
		return "", fmt.Errorf("sub-task failed: %w", err)
	}

	// Save to file
	if outputFile != "" {
		if saveErr := saveResultToFile(outputFile, resultText); saveErr != nil {
			log.Printf("[subtask] failed to save to %s: %v\n", outputFile, saveErr)
		}
	}

	// Build summary for parent
	summary := resultText
	if len(summary) > subTaskSummaryLen {
		summary = summary[:subTaskSummaryLen] + "\n...[truncated]"
	}
	if outputFile != "" {
		summary += fmt.Sprintf("\n\nFull results saved to: %s", outputFile)
	}
	if subTabId != "" {
		summary += fmt.Sprintf("\nSub-task tab created: check tab \"Sub: %s\" for full conversation.", taskPreview)
	}

	// Publish completed status
	if subTabId != "" {
		tabORef := waveobj.MakeORef(waveobj.OType_Tab, subTabId)
		wstore.SetRTInfo(tabORef, map[string]any{"waveai:subtask-status": "completed"})
		truncatedSummary := summary
		if len(truncatedSummary) > 500 {
			truncatedSummary = truncatedSummary[:500]
		}
		publishSubTaskEvent(subTabId, &wps.SubTaskUpdateData{
			ChatId:   subChatId,
			ParentId: parentOpts.ChatId,
			Status:   "completed",
			Summary:  truncatedSummary,
		})
	}

	log.Printf("[subtask] completed chat=%s result_len=%d\n", subChatId, len(resultText))
	return summary, nil
}

// extractLastAssistantText gets the last assistant message text from a chat.
// If the assistant never produced final text (e.g., canceled mid-work), falls back
// to collecting tool result data so the parent chat gets the information that was gathered.
func extractLastAssistantText(backend UseChatBackend, chatId string) string {
	chat := chatstore.DefaultChatStore.Get(chatId)
	if chat == nil {
		return ""
	}
	uiChat, err := backend.ConvertAIChatToUIChat(*chat)
	if err != nil {
		return ""
	}
	// Walk backwards to find the last assistant message with actual text content
	for i := len(uiChat.Messages) - 1; i >= 0; i-- {
		if uiChat.Messages[i].Role == "assistant" {
			text := uiChat.Messages[i].GetContent()
			// Skip empty text or text that's only a <think> block (no real output)
			text = strings.TrimSpace(text)
			if text != "" && !strings.HasPrefix(text, "<think>") {
				return text
			}
		}
	}

	// Fallback: no usable assistant text found.
	// Collect significant tool results so the gathered data isn't lost.
	log.Printf("[subtask] no assistant text found for chat=%s, collecting tool results as fallback\n", chatId)
	return extractToolResultsSummary(uiChat)
}

// extractToolResultsSummary collects tool outputs from the conversation
// as a fallback when the assistant didn't produce a final text response.
func extractToolResultsSummary(uiChat *uctypes.UIChat) string {
	var sb strings.Builder
	sb.WriteString("## Collected Data (assistant did not produce final text)\n\n")
	resultCount := 0
	for _, msg := range uiChat.Messages {
		for _, part := range msg.Parts {
			// Tool result parts have Type like "tool-*" and Output set
			if !strings.HasPrefix(part.Type, "tool-") {
				continue
			}
			if part.State != "output-available" {
				continue
			}
			outputStr := ""
			switch v := part.Output.(type) {
			case string:
				outputStr = v
			default:
				if part.Output != nil {
					bytes, err := json.Marshal(part.Output)
					if err == nil {
						outputStr = string(bytes)
					}
				}
			}
			if outputStr == "" || len(outputStr) < 20 {
				continue // skip trivial results like "true", "{"ok":true}"
			}
			// Truncate very long results
			if len(outputStr) > 2000 {
				outputStr = outputStr[:2000] + "\n...[truncated]"
			}
			toolName := strings.TrimPrefix(part.Type, "tool-")
			sb.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", toolName, outputStr))
			resultCount++
		}
	}
	if resultCount == 0 {
		return ""
	}
	return sb.String()
}

// findWorkspaceForTab finds the workspace that contains the given tab.
func findWorkspaceForTab(ctx context.Context, tabId string) (string, error) {
	if tabId == "" {
		return "", fmt.Errorf("no tabId")
	}
	// Query all workspaces directly (not via ListWorkspaces which filters by Name/Icon/Color)
	workspaces, err := wstore.DBGetAllObjsByType[*waveobj.Workspace](ctx, waveobj.OType_Workspace)
	if err != nil {
		return "", err
	}
	for _, ws := range workspaces {
		for _, tid := range ws.TabIds {
			if tid == tabId {
				return ws.OID, nil
			}
		}
	}
	return "", fmt.Errorf("workspace not found for tab %s", tabId)
}

// saveResultToFile writes text to a file, creating parent directories as needed.
func saveResultToFile(filePath string, content string) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(content), 0644)
}
