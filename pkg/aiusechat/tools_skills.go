// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/woveterm/wove/pkg/aiusechat/skills"
	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
)

func GetInvokeSkillToolDefinition(tabId string) uctypes.ToolDefinition {
	// Discover skills now to build the enum + description
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cwd := getTerminalCwd(ctx, tabId)
	allSkills := skills.DiscoverSkills(cwd)

	// Build enum list and short descriptions for the tool definition
	var skillNames []any
	var descLines []string
	for _, s := range allSkills {
		if !s.UserInvocable {
			continue
		}
		skillNames = append(skillNames, s.Name)
		hint := ""
		if s.ArgumentHint != "" {
			hint = " " + s.ArgumentHint
		}
		// Truncate description to first sentence
		desc := s.Description
		if idx := strings.Index(desc, "."); idx > 0 && idx < 120 {
			desc = desc[:idx+1]
		} else if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		descLines = append(descLines, fmt.Sprintf("- %s%s: %s", s.Name, hint, desc))
	}

	description := "Load and execute a skill (specialized instruction set). " +
		"Call this tool when the user asks for a task that matches an available skill. " +
		"The skill instructions will be returned — follow them carefully.\n\nAvailable skills:\n" +
		strings.Join(descLines, "\n")

	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Name of the skill to invoke.",
				"enum":        skillNames,
			},
			"arguments": map[string]any{
				"type":        "string",
				"description": "Arguments to pass to the skill (e.g., a URL or command).",
			},
		},
		"required": []string{"name"},
	}

	return uctypes.ToolDefinition{
		Name:             "invoke_skill",
		DisplayName:      "Invoke Skill",
		Description:      description,
		ShortDescription: "Load and execute a specialized skill",
		ToolLogName:      "skill:invoke",
		InputSchema:      inputSchema,
		ToolTextCallback: makeInvokeSkillCallback(tabId),
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			name, _ := inputMap["name"].(string)
			args, _ := inputMap["arguments"].(string)
			if args != "" {
				return fmt.Sprintf("invoking skill %q with args: %s", name, args)
			}
			return fmt.Sprintf("invoking skill %q", name)
		},
	}
}

func makeInvokeSkillCallback(tabId string) func(any) (string, error) {
	return func(input any) (string, error) {
		inputMap, ok := input.(map[string]any)
		if !ok {
			return "", fmt.Errorf("invalid input")
		}

		skillName, _ := inputMap["name"].(string)
		if skillName == "" {
			return "", fmt.Errorf("skill name is required")
		}
		rawArgs, _ := inputMap["arguments"].(string)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cwd := getTerminalCwd(ctx, tabId)

		loaded, err := skills.LoadSkillContent(cwd, skillName)
		if err != nil {
			return "", fmt.Errorf("skill %q not found: %w", skillName, err)
		}

		body := skills.SubstituteArgs(loaded.Body, rawArgs)

		// Truncate very long skills to avoid blowing up context
		const maxLen = 16000
		if len(body) > maxLen {
			body = body[:maxLen] + "\n...(truncated)"
		}

		result := fmt.Sprintf("# Skill: %s\n\n", skillName)
		result += "Execute ALL steps autonomously from start to finish. NEVER stop to ask the user \"should I continue?\", \"what would you like next?\", \"shall I proceed?\", or any variation. NEVER present a list of options and wait for selection. Complete the ENTIRE task end-to-end without pausing for confirmation.\n\n"
		result += "IMPORTANT: All tools mentioned below (web_open, web_read_text, web_read_html, web_seo_audit, web_exec_js, web_click, web_type_input, web_press_key, term_run_command, etc.) are AI tool calls that you invoke directly — they are NOT shell commands. Do NOT use bash/terminal to run them. Do NOT use sleep, curl, or other CLI tools when a dedicated AI tool exists.\n\n"
		result += "Follow these instructions carefully:\n\n"
		result += body

		return result, nil
	}
}
