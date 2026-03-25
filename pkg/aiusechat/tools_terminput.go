// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/blockcontroller"
	"github.com/woveterm/wove/pkg/util/utilfn"
	"github.com/woveterm/wove/pkg/wcore"
)

type TermSendInputInput struct {
	WidgetId   string `json:"widget_id"`
	Text       string `json:"text"`
	PressEnter *bool  `json:"press_enter,omitempty"`
}

type TermSendInputOutput struct {
	Ok bool `json:"ok"`
}

func parseTermSendInputInput(input any) (*TermSendInputInput, error) {
	result := &TermSendInputInput{}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	if err := utilfn.ReUnmarshal(result, input); err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}

	if result.WidgetId == "" {
		return nil, fmt.Errorf("widget_id is required")
	}

	if result.Text == "" && (result.PressEnter != nil && !*result.PressEnter) {
		return nil, fmt.Errorf("text is required when press_enter is false")
	}

	return result, nil
}

func GetTermSendInputToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "term_send_input",
		DisplayName: "Send Input to Terminal",
		Description: "Send text to the stdin of a terminal and press Enter (by default). Use for interactive programs (claude, vim, ssh, REPLs, etc.) and shell commands when you don't need to wait for output. Use term_get_scrollback to read the result afterward. Set press_enter=false only for raw keystrokes or partial input (e.g. Ctrl+C via \\x03). Does not require shell integration.",
		ToolLogName: "term:sendinput",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the terminal widget",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Text to send to the terminal. A newline is appended automatically unless press_enter is false. Can be empty or omitted to just press Enter.",
				},
				"press_enter": map[string]any{
					"type":        "boolean",
					"description": "Whether to append a newline (Enter) after the text. Defaults to true. Set to false for raw input like Ctrl+C (\\x03), Ctrl+D (\\x04), or partial text.",
				},
			},
			"required":             []string{"widget_id"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseTermSendInputInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			textStr := parsed.Text
			if len(textStr) > 60 {
				textStr = textStr[:57] + "..."
			}
			if output != nil {
				return fmt.Sprintf("sent input to %s", parsed.WidgetId)
			}
			return fmt.Sprintf("sending input to %s", parsed.WidgetId)
		},
		ToolApproval: func(input any) string {
			return uctypes.ApprovalNeedsApproval
		},
		ToolVerifyInput: func(input any, toolUseData *uctypes.UIMessageDataToolUse) error {
			parsed, err := parseTermSendInputInput(input)
			if err != nil {
				return err
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelFn()

			_, err = wcore.ResolveBlockIdFromPrefix(ctx, tabId, parsed.WidgetId)
			if err != nil {
				return fmt.Errorf("terminal widget not found: %w", err)
			}

			return nil
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			parsed, err := parseTermSendInputInput(input)
			if err != nil {
				return nil, err
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelFn()

			fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, parsed.WidgetId)
			if err != nil {
				return nil, fmt.Errorf("terminal widget not found: %w", err)
			}

			// Interpret escape sequences: \n → newline, \t → tab, \xHH → hex byte
			text, err := strconv.Unquote(`"` + parsed.Text + `"`)
			if err != nil {
				// If unquoting fails (e.g. text already contains real control chars), use as-is
				text = parsed.Text
			}

			// Append carriage return by default (press_enter defaults to true)
			// Enter key in a terminal sends \r, not \n
			pressEnter := parsed.PressEnter == nil || *parsed.PressEnter
			if pressEnter {
				text += "\r"
			}

			inputData := []byte(text)
			inputUnion := &blockcontroller.BlockInputUnion{
				InputData: inputData,
			}
			err = blockcontroller.SendInput(fullBlockId, inputUnion)
			if err != nil {
				return nil, fmt.Errorf("failed to send input to terminal: %w", err)
			}

			return &TermSendInputOutput{Ok: true}, nil
		},
	}
}
