// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/blockcontroller"
	"github.com/woveterm/wove/pkg/util/utilfn"
	"github.com/woveterm/wove/pkg/waveobj"
	"github.com/woveterm/wove/pkg/wcore"
	"github.com/woveterm/wove/pkg/wshrpc"
	"github.com/woveterm/wove/pkg/wshrpc/wshclient"
	"github.com/woveterm/wove/pkg/wshutil"
	"github.com/woveterm/wove/pkg/wstore"
)

const (
	TermRunCommandTimeout = 60 * time.Second
	TermRunMaxOutputLines = 1000
)

type TermRunCommandInput struct {
	WidgetId string `json:"widget_id"`
	Command  string `json:"command"`
}

type TermRunCommandOutput struct {
	Command   string `json:"command"`
	ExitCode  *int   `json:"exitcode,omitempty"`
	Output    string `json:"output"`
	TimedOut  bool   `json:"timedout,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	GitBranch string `json:"git_branch,omitempty"`
}

func parseTermRunCommandInput(input any) (*TermRunCommandInput, error) {
	result := &TermRunCommandInput{}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	if err := utilfn.ReUnmarshal(result, input); err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}

	// widget_id is optional — if empty, a terminal will be auto-created
	if result.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	return result, nil
}

const shellReadyTimeout = 20 * time.Second

// createTerminalWidget creates a new terminal widget in the given tab and waits for shell integration to become ready.
func createTerminalWidget(tabId string, owned *uctypes.OwnedWidgetSet) (string, error) {
	rpcClient := wshclient.GetBareRpcClient()
	oref, err := wshclient.CreateBlockCommand(rpcClient, wshrpc.CommandCreateBlockData{
		TabId: tabId,
		BlockDef: &waveobj.BlockDef{
			Meta: map[string]any{
				waveobj.MetaKey_View: "term",
			},
		},
		Focused: false,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create terminal widget: %w", err)
	}

	fullBlockId := oref.OID
	if owned != nil {
		owned.Add(fullBlockId)
	}

	// Wait for shell integration to become ready
	blockORef := waveobj.MakeORef(waveobj.OType_Block, fullBlockId)
	waitCtx, cancel := context.WithTimeout(context.Background(), shellReadyTimeout)
	defer cancel()

	watchCh, unsub := wstore.WatchRTInfoShellState(blockORef)
	defer unsub()

	// Check if already ready
	rtInfo := wstore.GetRTInfo(blockORef)
	if rtInfo != nil && rtInfo.ShellIntegration && rtInfo.ShellState == "ready" {
		return fullBlockId, nil
	}

	for {
		select {
		case <-waitCtx.Done():
			log.Printf("[term] shell integration not ready after %v, proceeding anyway\n", shellReadyTimeout)
		return fullBlockId, nil
		case update := <-watchCh:
			if update != nil && update.ShellState == "ready" {
				return fullBlockId, nil
			}
		}
	}
}

// resolveOrCreateTerminal resolves an existing terminal widget or creates a new one if widget_id is empty.
func resolveOrCreateTerminal(tabId string, widgetId string, owned *uctypes.OwnedWidgetSet) (string, error) {
	if widgetId != "" {
		ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelFn()
		return wcore.ResolveBlockIdFromPrefix(ctx, tabId, widgetId)
	}

	// No widget_id provided — try to find an existing terminal in the tab, otherwise create one
	ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFn()

	tabObj, err := wstore.DBMustGet[*waveobj.Tab](ctx, tabId)
	if err == nil {
		for _, blockId := range tabObj.BlockIds {
			block, err := wstore.DBGet[*waveobj.Block](ctx, blockId)
			if err != nil || block == nil || block.Meta == nil {
				continue
			}
			viewType, _ := block.Meta["view"].(string)
			if viewType != "term" {
				continue
			}
			// Found a terminal — check if it has shell integration and is ready
			blockORef := waveobj.MakeORef(waveobj.OType_Block, blockId)
			rtInfo := wstore.GetRTInfo(blockORef)
			if rtInfo != nil && rtInfo.ShellIntegration && rtInfo.ShellState == "ready" {
				return blockId, nil
			}
		}
	}

	// No usable terminal found — create one
	return createTerminalWidget(tabId, owned)
}

// getGitBranch reads the current git branch from .git/HEAD in the given directory or its parents.
func getGitBranch(dir string) string {
	for d := dir; d != "/" && d != "."; d = filepath.Dir(d) {
		headPath := filepath.Join(d, ".git", "HEAD")
		data, err := os.ReadFile(headPath)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if strings.HasPrefix(content, "ref: refs/heads/") {
			return strings.TrimPrefix(content, "ref: refs/heads/")
		}
		// Detached HEAD — return short hash
		if len(content) >= 8 {
			return content[:8]
		}
		return content
	}
	return ""
}

func sendCommandToTerminal(blockId string, command string) error {
	// Send the command text followed by a newline (Enter key)
	inputData := []byte(command + "\n")
	inputUnion := &blockcontroller.BlockInputUnion{
		InputData: inputData,
	}
	return blockcontroller.SendInput(blockId, inputUnion)
}

func waitForCommandCompletion(ctx context.Context, blockORef waveobj.ORef) (bool, error) {
	// Check if already complete before subscribing
	rtInfo := wstore.GetRTInfo(blockORef)
	if rtInfo == nil {
		return false, fmt.Errorf("terminal runtime info not available")
	}
	if !rtInfo.ShellIntegration {
		return false, fmt.Errorf("shell integration is not enabled for this terminal")
	}
	if rtInfo.ShellState == "ready" {
		return true, nil
	}

	// Subscribe to shell state changes for this block
	watchCh, unsub := wstore.WatchRTInfoShellState(blockORef)
	defer unsub()

	// Check again after subscribing to avoid race condition
	rtInfo = wstore.GetRTInfo(blockORef)
	if rtInfo != nil && rtInfo.ShellState == "ready" {
		return true, nil
	}

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case update := <-watchCh:
			if update != nil && update.ShellState == "ready" {
				return true, nil
			}
			// ShellState changed but not to "ready" yet, keep waiting
		}
	}
}

func GetTermRunCommandToolDefinition(tabId string, ownedWidgets ...*uctypes.OwnedWidgetSet) uctypes.ToolDefinition {
	var owned *uctypes.OwnedWidgetSet
	if len(ownedWidgets) > 0 {
		owned = ownedWidgets[0]
	}
	return uctypes.ToolDefinition{
		Name:        "term_run_command",
		DisplayName: "Run Terminal Command",
		Description: "Run a short-lived command in terminal and return output. 60s timeout. For interactive/long-running programs use term_send_input. Auto-selects terminal if widget_id omitted.",
		ToolLogName: "term:runcommand",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the terminal widget. Optional — if omitted, uses an existing ready terminal or auto-creates one.",
				},
				"command": map[string]any{
					"type":        "string",
					"description": "The command to execute in the terminal (e.g., 'php artisan migrate:status', 'composer validate', 'ls -la')",
				},
			},
			"required": []string{"command"},
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseTermRunCommandInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			cmdStr := parsed.Command
			if len(cmdStr) > 60 {
				cmdStr = cmdStr[:57] + "..."
			}
			if output != nil {
				if parsed.WidgetId != "" {
					return fmt.Sprintf("ran `%s` in %s", cmdStr, parsed.WidgetId)
				}
				return fmt.Sprintf("ran `%s`", cmdStr)
			}
			if parsed.WidgetId != "" {
				return fmt.Sprintf("running `%s` in %s", cmdStr, parsed.WidgetId)
			}
			return fmt.Sprintf("running `%s`", cmdStr)
		},
		ToolApproval: func(input any) string {
			return uctypes.ApprovalNeedsApproval
		},
		ToolVerifyInput: func(input any, toolUseData *uctypes.UIMessageDataToolUse) error {
			parsed, err := parseTermRunCommandInput(input)
			if err != nil {
				return err
			}

			// If no widget_id, we'll auto-create in the callback — skip verify
			if parsed.WidgetId == "" {
				return nil
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelFn()

			fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, parsed.WidgetId)
			if err != nil {
				return fmt.Errorf("terminal widget not found: %w", err)
			}

			blockORef := waveobj.MakeORef(waveobj.OType_Block, fullBlockId)
			rtInfo := wstore.GetRTInfo(blockORef)
			if rtInfo == nil {
				return fmt.Errorf("terminal runtime info not available")
			}
			if !rtInfo.ShellIntegration {
				return fmt.Errorf("shell integration is not enabled for this terminal — it is required to track command execution")
			}
			if rtInfo.ShellState == "running-command" {
				return fmt.Errorf("terminal is currently running another command, wait for it to finish first")
			}

			return nil
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			parsed, err := parseTermRunCommandInput(input)
			if err != nil {
				return nil, err
			}

			fullBlockId, err := resolveOrCreateTerminal(tabId, parsed.WidgetId, owned)
			if err != nil {
				return nil, fmt.Errorf("terminal widget not found: %w", err)
			}

			blockORef := waveobj.MakeORef(waveobj.OType_Block, fullBlockId)
			rtInfo := wstore.GetRTInfo(blockORef)
			if rtInfo == nil {
				return nil, fmt.Errorf("terminal runtime info not available")
			}
			if !rtInfo.ShellIntegration {
				return nil, fmt.Errorf("shell integration is not enabled for this terminal")
			}
			if rtInfo.ShellState == "running-command" {
				return nil, fmt.Errorf("terminal is currently running another command")
			}

			// Send the command to the terminal
			err = sendCommandToTerminal(fullBlockId, parsed.Command)
			if err != nil {
				return nil, fmt.Errorf("failed to send command to terminal: %w", err)
			}

			// Wait briefly for the command to start
			time.Sleep(100 * time.Millisecond)

			// Wait for the command to complete with a timeout
			waitCtx, waitCancel := context.WithTimeout(context.Background(), TermRunCommandTimeout)
			defer waitCancel()

			completed, err := waitForCommandCompletion(waitCtx, blockORef)

			// Read the output regardless of whether it completed or timed out
			rpcClient := wshclient.GetBareRpcClient()
			scrollbackResult, scrollErr := wshclient.TermGetScrollbackLinesCommand(
				rpcClient,
				wshrpc.CommandTermGetScrollbackLinesData{
					LastCommand: true,
				},
				&wshrpc.RpcOpts{Route: wshutil.MakeFeBlockRouteId(fullBlockId)},
			)

			output := &TermRunCommandOutput{
				Command: parsed.Command,
			}

			if err != nil && !completed {
				output.TimedOut = true
			}

			// Get exit code from RTInfo
			latestRtInfo := wstore.GetRTInfo(blockORef)
			if latestRtInfo != nil && latestRtInfo.ShellState == "ready" {
				exitCode := latestRtInfo.ShellLastCmdExitCode
				output.ExitCode = &exitCode
			}

			if scrollErr != nil {
				output.Output = fmt.Sprintf("[command executed but output could not be read: %v]", scrollErr)
			} else if scrollbackResult != nil {
				lines := scrollbackResult.Lines
				if len(lines) > TermRunMaxOutputLines {
					lines = lines[len(lines)-TermRunMaxOutputLines:]
				}
				output.Output = strings.Join(lines, "\n")
			}

			// Enrich output with CWD and git branch for stateful context
			cwd := getTerminalCwd(context.Background(), tabId)
			if cwd != "" {
				output.Cwd = cwd
				if branch := getGitBranch(cwd); branch != "" {
					output.GitBranch = branch
				}
			}

			return output, nil
		},
	}
}
