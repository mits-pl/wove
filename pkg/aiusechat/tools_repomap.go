// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"fmt"

	"github.com/woveterm/wove/pkg/aiusechat/repomap"
	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/util/utilfn"
	"github.com/woveterm/wove/pkg/wavebase"
)

type repoMapParams struct {
	Path     string `json:"path"`
	Kind     string `json:"kind,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
}

func parseRepoMapInput(input any) (*repoMapParams, error) {
	result := &repoMapParams{}
	if input == nil {
		return nil, fmt.Errorf("input is required")
	}
	if err := utilfn.ReUnmarshal(result, input); err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}
	if result.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if result.MaxChars <= 0 {
		result.MaxChars = 12000
	}
	if result.MaxChars > 30000 {
		result.MaxChars = 30000
	}
	return result, nil
}

func repoMapCallback(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
	params, err := parseRepoMapInput(input)
	if err != nil {
		return nil, err
	}

	expandedPath, err := wavebase.ExpandHomeDir(params.Path)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	result := repomap.BuildRepoMap(expandedPath, params.MaxChars)
	if result == "" {
		return "No code definitions found in this directory.", nil
	}

	if params.Kind != "" {
		result = repomap.FilterByKind(result, params.Kind)
		if result == "" {
			return fmt.Sprintf("No %q definitions found in this directory.", params.Kind), nil
		}
	}

	return result, nil
}

func GetRepoMapToolDefinition() uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "repo_map",
		DisplayName: "Repository Map",
		Description: "Get a structural map of code definitions (functions, classes, methods, types, interfaces) in a directory using tree-sitter parsing. More precise than grep for understanding code structure. Supports Go, PHP, JS, TS, TSX, Python, Rust, Vue.",
		ToolLogName: "gen:repomap",
		Strict:      false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the directory to scan. Supports '~' for the user's home directory.",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "Optional filter by definition kind: func, method, class, type, interface, enum. Returns only matching definitions.",
					"enum":        []string{"func", "method", "class", "type", "interface", "enum"},
				},
				"max_chars": map[string]any{
					"type":        "integer",
					"minimum":     1000,
					"maximum":     30000,
					"default":     12000,
					"description": "Maximum output size in characters. Defaults to 12000, max 30000. Use larger values for big directories.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			params, err := parseRepoMapInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			if params.Kind != "" {
				return fmt.Sprintf("scanning %q for %s definitions", params.Path, params.Kind)
			}
			return fmt.Sprintf("scanning %q for code definitions", params.Path)
		},
		ToolAnyCallback: repoMapCallback,
		ToolApproval: func(input any) string {
			return uctypes.ApprovalAutoApproved
		},
	}
}
