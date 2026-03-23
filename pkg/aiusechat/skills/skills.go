// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SkillManifest holds parsed frontmatter from a SKILL.md file.
type SkillManifest struct {
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	AllowedTools           []string `json:"allowedtools,omitempty"`
	ArgumentHint           string   `json:"argumenthint,omitempty"`
	DisableModelInvocation bool     `json:"disablemodelinvocation,omitempty"`
	UserInvocable          bool     `json:"userinvocable"`
	FilePath               string   `json:"filepath"`
}

// LoadedSkill is a fully loaded skill with manifest and body content.
type LoadedSkill struct {
	Manifest SkillManifest
	Body     string
}

// DiscoverSkills scans .claude/skills/*/SKILL.md in the given cwd and the user's
// home directory. Returns manifests (frontmatter only) for all discovered skills.
// Project skills take priority over personal skills with the same name.
func DiscoverSkills(cwd string) []SkillManifest {
	seen := make(map[string]bool)
	var result []SkillManifest

	// Project skills first (higher priority)
	if cwd != "" {
		projectDir := filepath.Join(cwd, ".claude", "skills")
		for _, m := range scanSkillsDir(projectDir) {
			if !seen[m.Name] {
				seen[m.Name] = true
				result = append(result, m)
			}
		}
	}

	// Personal/global skills
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		personalDir := filepath.Join(home, ".claude", "skills")
		for _, m := range scanSkillsDir(personalDir) {
			if !seen[m.Name] {
				seen[m.Name] = true
				result = append(result, m)
			}
		}
	}

	return result
}

// LoadSkillContent loads a skill by name, searching project dir then home dir.
func LoadSkillContent(cwd string, skillName string) (*LoadedSkill, error) {
	paths := skillSearchPaths(cwd, skillName)
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		manifest, body, err := parseSkillMd(string(data))
		if err != nil {
			continue
		}
		manifest.FilePath = p
		if manifest.Name == "" {
			manifest.Name = skillName
		}
		return &LoadedSkill{Manifest: manifest, Body: body}, nil
	}
	return nil, fmt.Errorf("skill %q not found", skillName)
}

// ParseSlashCommand parses "/skill-name arg1 arg2" from message text.
// Returns the skill name, raw arguments string, and whether it matched.
func ParseSlashCommand(text string) (skillName string, rawArgs string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	// Skip built-in commands
	if text == "/clear" || text == "/new" {
		return "", "", false
	}
	parts := strings.SplitN(text[1:], " ", 2)
	skillName = parts[0]
	if skillName == "" {
		return "", "", false
	}
	if len(parts) > 1 {
		rawArgs = strings.TrimSpace(parts[1])
	}
	return skillName, rawArgs, true
}

// SubstituteArgs replaces $ARGUMENTS and positional $0, $1, etc. in skill body.
func SubstituteArgs(body string, rawArgs string) string {
	body = strings.ReplaceAll(body, "$ARGUMENTS", rawArgs)

	args := splitArgs(rawArgs)
	// Replace $0, $1, ... $9
	for i := 0; i <= 9; i++ {
		placeholder := fmt.Sprintf("$%d", i)
		if !strings.Contains(body, placeholder) {
			continue
		}
		val := ""
		if i < len(args) {
			val = args[i]
		}
		body = strings.ReplaceAll(body, placeholder, val)
	}
	return body
}

// FormatSkillInvocation returns full skill instructions wrapped in XML tags,
// with arguments substituted.
func FormatSkillInvocation(skill *LoadedSkill, rawArgs string) string {
	body := SubstituteArgs(skill.Body, rawArgs)
	// Truncate very long skills to avoid blowing up context
	const maxLen = 16000
	if len(body) > maxLen {
		body = body[:maxLen] + "\n...(truncated)"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<skill name=%q arguments=%q>\n", skill.Manifest.Name, rawArgs))
	sb.WriteString("Follow these instructions carefully.\n\n")
	sb.WriteString("IMPORTANT: All tools mentioned in this skill (web_open, web_read_text, web_read_html, web_seo_audit, web_exec_js, web_click, web_type_input, web_press_key, term_run_command, etc.) are AI tool calls that you invoke directly — they are NOT shell commands. Do NOT use bash/terminal to run them. Do NOT use sleep, curl, or other CLI tools when a dedicated AI tool exists. Call the tools by their function name as tool invocations.\n\n")
	sb.WriteString(body)
	sb.WriteString("\n</skill>")
	return sb.String()
}

// --- internal helpers ---

func scanSkillsDir(dir string) []SkillManifest {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []SkillManifest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		manifest, _, err := parseSkillMd(string(data))
		if err != nil {
			continue
		}
		manifest.FilePath = skillFile
		if manifest.Name == "" {
			manifest.Name = entry.Name()
		}
		manifest.UserInvocable = manifest.UserInvocable // already defaulted in parse
		result = append(result, manifest)
	}
	return result
}

func skillSearchPaths(cwd string, skillName string) []string {
	var paths []string
	if cwd != "" {
		paths = append(paths, filepath.Join(cwd, ".claude", "skills", skillName, "SKILL.md"))
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".claude", "skills", skillName, "SKILL.md"))
	}
	return paths
}

// parseSkillMd parses a SKILL.md file with YAML-like frontmatter delimited by ---.
// Supports YAML features: multiline scalars (> and |), list items (- value),
// and comma-separated values. Returns manifest (from frontmatter) and body.
func parseSkillMd(content string) (SkillManifest, string, error) {
	manifest := SkillManifest{
		UserInvocable: true, // default
	}

	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		// No frontmatter — entire content is body, use directory name as skill name
		return manifest, content, nil
	}

	// Find closing ---
	rest := content[3:]
	if idx := strings.Index(rest, "\n"); idx >= 0 {
		rest = rest[idx+1:]
	} else {
		return manifest, "", fmt.Errorf("malformed frontmatter")
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return manifest, "", fmt.Errorf("unclosed frontmatter")
	}

	frontmatter := rest[:endIdx]
	body := strings.TrimSpace(rest[endIdx+4:])

	// Parse YAML-like frontmatter with support for multiline values and lists
	lines := strings.Split(frontmatter, "\n")
	var currentKey string
	var currentVal strings.Builder
	var currentList []string
	isList := false

	flushKey := func() {
		if currentKey == "" {
			return
		}
		if isList {
			applyManifestList(&manifest, currentKey, currentList)
		} else {
			val := strings.TrimSpace(currentVal.String())
			// Clean up YAML folded/literal scalar indicators
			val = strings.TrimPrefix(val, ">")
			val = strings.TrimPrefix(val, "|")
			val = strings.TrimSpace(val)
			applyManifestValue(&manifest, currentKey, val)
		}
		currentKey = ""
		currentVal.Reset()
		currentList = nil
		isList = false
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Check if this is a YAML list item (indented "- value")
		if strings.HasPrefix(line, "  ") && strings.HasPrefix(trimmed, "- ") {
			if currentKey != "" {
				isList = true
				item := strings.TrimSpace(trimmed[2:])
				currentList = append(currentList, item)
				continue
			}
		}

		// Check if this is a continuation line (indented, no colon at root level)
		if strings.HasPrefix(line, "  ") && currentKey != "" && !isList {
			if currentVal.Len() > 0 {
				currentVal.WriteString(" ")
			}
			currentVal.WriteString(trimmed)
			continue
		}

		// New top-level key
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}

		flushKey()

		currentKey = strings.TrimSpace(line[:colonIdx])
		val := strings.TrimSpace(line[colonIdx+1:])

		// Check for YAML multiline indicators (> or |)
		if val == ">" || val == "|" {
			// Value continues on next lines
			continue
		}
		currentVal.WriteString(val)
	}
	flushKey()

	return manifest, body, nil
}

func applyManifestValue(m *SkillManifest, key string, val string) {
	switch strings.ToLower(key) {
	case "name":
		m.Name = val
	case "description":
		m.Description = val
	case "allowed-tools":
		m.AllowedTools = splitComma(val)
	case "argument-hint":
		m.ArgumentHint = strings.Trim(val, "\"'")
	case "disable-model-invocation":
		m.DisableModelInvocation = parseBool(val)
	case "user-invocable", "user-invokable":
		m.UserInvocable = parseBool(val)
	}
}

func applyManifestList(m *SkillManifest, key string, items []string) {
	switch strings.ToLower(key) {
	case "allowed-tools":
		m.AllowedTools = items
	}
}

func splitComma(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "yes" || s == "1"
}

var argSplitRe = regexp.MustCompile(`\s+`)

func splitArgs(rawArgs string) []string {
	rawArgs = strings.TrimSpace(rawArgs)
	if rawArgs == "" {
		return nil
	}
	return argSplitRe.Split(rawArgs, -1)
}
