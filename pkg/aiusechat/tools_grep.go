// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/util/utilfn"
	"github.com/woveterm/wove/pkg/wavebase"
)

const (
	GrepTimeout        = 30 * time.Second
	GrepMaxOutputLines = 500
	GrepMaxOutputBytes = 100 * 1024
)

type grepParams struct {
	Pattern     string  `json:"pattern"`
	Path        string  `json:"path"`
	Include     *string `json:"include"`
	IgnoreCase  *bool   `json:"ignore_case"`
	MaxResults  *int    `json:"max_results"`
	ContextLines *int   `json:"context_lines"`
	FixedString *bool   `json:"fixed_string"`
}

func parseGrepInput(input any) (*grepParams, error) {
	result := &grepParams{}
	if input == nil {
		return nil, fmt.Errorf("input is required")
	}
	if err := utilfn.ReUnmarshal(result, input); err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}
	if result.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if result.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	return result, nil
}

func GetGrepToolDefinition() uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "grep",
		DisplayName: "Grep Search",
		Description: "Search file contents using grep. Runs silently in the background without using a terminal. Use this instead of running grep via term_run_command. Supports recursive search, file type filtering, and regex patterns.",
		ToolLogName: "gen:grep",
		Strict:      false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Search pattern (regex by default, use fixed_string=true for literal match)",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to file or directory to search in. Supports '~' for home directory.",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "File glob pattern to filter which files to search (e.g. '*.go', '*.{ts,tsx}', '*.php')",
				},
				"ignore_case": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Case-insensitive search",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"default":     GrepMaxOutputLines,
					"description": "Maximum number of matching lines to return",
				},
				"context_lines": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"default":     0,
					"description": "Number of context lines to show before and after each match",
				},
				"fixed_string": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Treat pattern as a fixed string instead of regex",
				},
			},
			"required":             []string{"pattern", "path"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseGrepInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			patternStr := parsed.Pattern
			if len(patternStr) > 40 {
				patternStr = patternStr[:37] + "..."
			}
			if output != nil {
				return fmt.Sprintf("searched for `%s` in %s", patternStr, parsed.Path)
			}
			return fmt.Sprintf("searching for `%s` in %s", patternStr, parsed.Path)
		},
		ToolAnyCallback: grepCallback,
	}
}

func grepCallback(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
	parsed, err := parseGrepInput(input)
	if err != nil {
		return nil, err
	}

	expandedPath, err := wavebase.ExpandHomeDir(parsed.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to expand path: %w", err)
	}

	maxResults := GrepMaxOutputLines
	if parsed.MaxResults != nil && *parsed.MaxResults > 0 {
		maxResults = *parsed.MaxResults
		if maxResults > GrepMaxOutputLines {
			maxResults = GrepMaxOutputLines
		}
	}

	// Build grep command args
	args := []string{"-r", "-n"} // recursive, line numbers

	if parsed.IgnoreCase != nil && *parsed.IgnoreCase {
		args = append(args, "-i")
	}

	if parsed.FixedString != nil && *parsed.FixedString {
		args = append(args, "-F")
	}

	if parsed.ContextLines != nil && *parsed.ContextLines > 0 {
		args = append(args, fmt.Sprintf("-C%d", *parsed.ContextLines))
	}

	args = append(args, fmt.Sprintf("-m%d", maxResults))

	if parsed.Include != nil && *parsed.Include != "" {
		args = append(args, "--include="+*parsed.Include)
	}

	// Exclude common noise directories
	args = append(args, "--exclude-dir=.git")
	args = append(args, "--exclude-dir=node_modules")
	args = append(args, "--exclude-dir=vendor")
	args = append(args, "--exclude-dir=.next")
	args = append(args, "--exclude-dir=dist")
	args = append(args, "--exclude-dir=build")

	args = append(args, "--", parsed.Pattern, expandedPath)

	ctx, cancel := context.WithTimeout(context.Background(), GrepTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "grep", args...)
	outputBytes, err := cmd.CombinedOutput()

	output := string(outputBytes)

	// Truncate if too large
	if len(output) > GrepMaxOutputBytes {
		output = output[:GrepMaxOutputBytes] + "\n...[output truncated]"
	}

	// grep returns exit code 1 when no matches found — that's not an error
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				return map[string]any{
					"matches": 0,
					"output":  "No matches found.",
				}, nil
			}
		}
		if ctx.Err() == context.DeadlineExceeded {
			return map[string]any{
				"output":    output,
				"timed_out": true,
			}, nil
		}
		return nil, fmt.Errorf("grep failed: %w\n%s", err, output)
	}

	// Count matches
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	matchCount := len(lines)

	return map[string]any{
		"matches": matchCount,
		"output":  output,
	}, nil
}
