// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/waveobj"
	"github.com/woveterm/wove/pkg/wcore"
	"github.com/woveterm/wove/pkg/wshrpc"
	"github.com/woveterm/wove/pkg/wshrpc/wshclient"
	"github.com/woveterm/wove/pkg/wstore"
)

func GetCloseWidgetToolDefinition(tabId string, ownedWidgets *uctypes.OwnedWidgetSet) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "close_widget",
		DisplayName:      "Close Widget",
		Description:      "Close a widget (terminal, web browser, preview, etc.) that you opened. Use this to clean up widgets you no longer need. You should close terminals and browsers you opened when you are done with them. You can only close widgets that you created — pre-existing user widgets cannot be closed.",
		ShortDescription: "Close a widget",
		ToolLogName:      "widget:close",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the widget to close",
				},
			},
			"required":             []string{"widget_id"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			return fmt.Sprintf("closing widget %s", widgetId)
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid input format")
			}
			widgetId, _ := inputMap["widget_id"].(string)
			if widgetId == "" {
				return nil, fmt.Errorf("widget_id is required")
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelFn()

			fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, widgetId)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve widget %q: %w", widgetId, err)
			}

			// Prevent closing the AI chat widget itself
			block, err := wstore.DBGet[*waveobj.Block](ctx, fullBlockId)
			if err != nil {
				return nil, fmt.Errorf("failed to get widget info: %w", err)
			}
			if block != nil && block.Meta != nil {
				if viewType, ok := block.Meta["view"].(string); ok && viewType == "waveai" {
					return nil, fmt.Errorf("cannot close the AI chat widget")
				}
			}

			// Ownership check: only allow closing widgets that this chat created
			if ownedWidgets != nil && !ownedWidgets.Contains(fullBlockId) {
				return nil, fmt.Errorf("cannot close widget %q: you can only close widgets that you opened", widgetId)
			}

			rpcClient := wshclient.GetBareRpcClient()
			err = wshclient.DeleteBlockCommand(rpcClient, wshrpc.CommandDeleteBlockData{
				BlockId: fullBlockId,
			}, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to close widget %q: %w", widgetId, err)
			}

			// Remove from owned set
			if ownedWidgets != nil {
				ownedWidgets.Remove(fullBlockId)
			}

			return map[string]any{
				"status":    "closed",
				"widget_id": widgetId,
			}, nil
		},
	}
}

// CleanupOwnedWidgets closes all widgets owned by the given set.
// Used for automatic cleanup when a subtask finishes.
func CleanupOwnedWidgets(ownedWidgets *uctypes.OwnedWidgetSet) {
	if ownedWidgets == nil {
		return
	}
	blockIds := ownedWidgets.GetAll()
	if len(blockIds) == 0 {
		return
	}
	log.Printf("[cleanup] closing %d owned widgets\n", len(blockIds))
	rpcClient := wshclient.GetBareRpcClient()
	for _, blockId := range blockIds {
		err := wshclient.DeleteBlockCommand(rpcClient, wshrpc.CommandDeleteBlockData{
			BlockId: blockId,
		}, nil)
		if err != nil {
			log.Printf("[cleanup] failed to close widget %s: %v\n", blockId[:8], err)
		} else {
			log.Printf("[cleanup] closed widget %s\n", blockId[:8])
		}
		ownedWidgets.Remove(blockId)
	}
}
