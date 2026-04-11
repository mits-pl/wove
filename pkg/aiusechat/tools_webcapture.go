// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/wcore"
	"github.com/woveterm/wove/pkg/wshrpc"
	"github.com/woveterm/wove/pkg/wshrpc/wshclient"
	"github.com/woveterm/wove/pkg/wshutil"
)

// runWebCapture performs the SoM capture RPC and returns the built text summary + screenshot data URL.
func runWebCapture(tabId string, input any) (textOut string, screenshot string, err error) {
	inputMap, ok := input.(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("invalid input format")
	}
	widgetId, _ := inputMap["widget_id"].(string)
	if widgetId == "" {
		return "", "", fmt.Errorf("widget_id is required")
	}

	ctx, cancelFn := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelFn()

	fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, widgetId)
	if err != nil {
		return "", "", err
	}

	rpcClient := wshclient.GetBareRpcClient()
	blockInfo, err := wshclient.BlockInfoCommand(rpcClient, fullBlockId, nil)
	if err != nil {
		return "", "", fmt.Errorf("getting block info: %w", err)
	}

	captureData := wshrpc.CommandWebCaptureData{
		WorkspaceId: blockInfo.WorkspaceId,
		BlockId:     fullBlockId,
		TabId:       blockInfo.TabId,
	}
	result, err := wshclient.WebCaptureCommand(rpcClient, captureData, &wshrpc.RpcOpts{
		Route:   wshutil.ElectronRoute,
		Timeout: 15000,
	})
	if err != nil {
		return "", "", fmt.Errorf("web capture failed: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Viewport: %dx%d, scroll: %d/%d\n",
		result.Viewport.Width, result.Viewport.Height,
		result.Viewport.ScrollY, result.Viewport.PageHeight))
	sb.WriteString(fmt.Sprintf("Elements (%d):\n", len(result.Elements)))
	for _, el := range result.Elements {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", el.Idx, el.Desc))
	}

	return sb.String(), result.Screenshot, nil
}

// GetWebCaptureToolDefinition returns a web_capture tool definition tailored to model capabilities.
// When the model supports images, both a JPEG screenshot and the element list are returned.
// When the model is text-only (e.g. MiniMax), only the element list + viewport info are returned.
func GetWebCaptureToolDefinition(tabId string, capabilities []string) uctypes.ToolDefinition {
	hasImages := slices.Contains(capabilities, uctypes.AICapabilityImages)

	var description string
	if hasImages {
		description = "Capture a visual snapshot of a web page. Returns a JPEG screenshot with numbered element markers (SoM) and a structured list of interactive elements with coordinates and CSS selectors. Use this to see the page layout and identify elements for clicking (web_click) or typing (web_type_input). Each element includes a CSS selector you can use directly with other web tools."
	} else {
		description = "Capture a text summary of a web page. Returns viewport info and a structured list of interactive elements (links, buttons, inputs) with numbered markers and CSS selectors. Use this to understand page layout and identify elements for clicking (web_click) or typing (web_type_input) without needing vision. For full content, combine with web_read_text."
	}

	def := uctypes.ToolDefinition{
		Name:             "web_capture",
		DisplayName:      "Capture Web Page",
		Description:      description,
		ShortDescription: "Screenshot + elements from web widget",
		ToolLogName:      "web:capture",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
			},
			"required":             []string{"widget_id"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return "error parsing input: invalid format"
			}
			widgetId, ok := inputMap["widget_id"].(string)
			if !ok {
				return "error parsing input: missing widget_id"
			}
			return fmt.Sprintf("capturing web page snapshot from widget %s", widgetId)
		},
	}

	if hasImages {
		def.ToolImageTextCallback = func(input any) (string, string, error) {
			return runWebCapture(tabId, input)
		}
	} else {
		def.ToolTextCallback = func(input any) (string, error) {
			text, _, err := runWebCapture(tabId, input)
			return text, err
		}
	}

	return def
}
