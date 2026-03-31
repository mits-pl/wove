// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package projectctx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Supported instruction file names, checked in order of priority.
var instructionFiles = []string{
	"WAVE.md",
	"CLAUDE.md",
	"GEMINI.md",
	"AGENTS.md",
	".cursorrules",
	".github/copilot-instructions.md",
}

// Section represents a parsed section of the instructions file.
type Section struct {
	Heading string
	Content string
	Tags    []string // inferred technology tags
}

// ProjectInstructions holds parsed project instructions.
type ProjectInstructions struct {
	FilePath    string
	FileName    string
	RawSize     int
	Sections    []Section
	ProjectInfo string // first section (Project/overview)
}

// FindInstructionsFile looks for a known instructions file in the given directory.
// Returns the first found file path (for backward compat / system prompt check).
func FindInstructionsFile(dir string) string {
	for _, name := range instructionFiles {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// FindAllInstructionsFiles returns all existing instructions files in the directory.
// WAVE.md is always first (highest priority), then others in order.
func FindAllInstructionsFiles(dir string) []string {
	var found []string
	for _, name := range instructionFiles {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	return found
}

// ParseInstructions reads and parses an instructions file into sections.
func ParseInstructions(filePath string) (*ProjectInstructions, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading instructions: %w", err)
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	pi := &ProjectInstructions{
		FilePath: filePath,
		FileName: filepath.Base(filePath),
		RawSize:  len(data),
	}

	var currentSection *Section
	var sectionLines []string

	flushSection := func() {
		if currentSection != nil {
			currentSection.Content = strings.TrimSpace(strings.Join(sectionLines, "\n"))
			currentSection.Tags = inferTags(currentSection.Heading, currentSection.Content)
			pi.Sections = append(pi.Sections, *currentSection)
		}
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			flushSection()
			heading := strings.TrimPrefix(line, "## ")
			currentSection = &Section{Heading: heading}
			sectionLines = nil
		} else if strings.HasPrefix(line, "# ") && currentSection == nil {
			// Top-level heading, skip but capture as project info start
			continue
		} else {
			sectionLines = append(sectionLines, line)
		}
	}
	flushSection()

	// Extract project info from first section
	if len(pi.Sections) > 0 && isOverviewSection(pi.Sections[0].Heading) {
		pi.ProjectInfo = pi.Sections[0].Content
	}

	return pi, nil
}

// GetFilteredContext returns sections relevant to the given file extension/technology.
// Always includes: Project overview, Architecture, Conventions.
// Adds technology-specific sections based on the file being edited.
func GetFilteredContext(pi *ProjectInstructions, fileExt string) string {
	if pi == nil || len(pi.Sections) == 0 {
		return ""
	}

	techTags := extToTags(fileExt)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<project_instructions source=%q>\n", pi.FileName))
	sb.WriteString("IMPORTANT: These project-specific instructions OVERRIDE your general knowledge and default behavior. When these rules conflict with your training, follow THESE rules exactly as written.\n\n")

	for _, section := range pi.Sections {
		if shouldInclude(section, techTags) {
			sb.WriteString(fmt.Sprintf("## %s\n", section.Heading))
			sb.WriteString(section.Content)
			sb.WriteString("\n\n")
		}
	}

	sb.WriteString("</project_instructions>")
	result := sb.String()

	// Truncate if too long
	const maxLen = 30000
	if len(result) > maxLen {
		result = result[:maxLen] + "\n... [truncated]\n</project_instructions>"
	}

	return result
}

// GetFullContext returns all sections (with truncation).
func GetFullContext(pi *ProjectInstructions) string {
	return GetFilteredContext(pi, "")
}

// alwaysInclude are section heading keywords that are always relevant.
var alwaysIncludeKeywords = []string{
	"project", "stack", "architektura", "architecture",
	"conventions", "konwencje", "replies", "structure",
	"foundational", "overview", "opis",
}

func isOverviewSection(heading string) bool {
	h := strings.ToLower(heading)
	return strings.Contains(h, "project") || strings.Contains(h, "overview") ||
		strings.Contains(h, "opis") || strings.Contains(h, "stack")
}

// SectionMatchesExt checks if a section is relevant to the given file extension.
func SectionMatchesExt(section Section, fileExt string) bool {
	techTags := extToTags(fileExt)
	return shouldInclude(section, techTags)
}

func shouldInclude(section Section, techTags []string) bool {
	h := strings.ToLower(section.Heading)

	// Always include core sections
	for _, kw := range alwaysIncludeKeywords {
		if strings.Contains(h, kw) {
			return true
		}
	}

	// If no tech filter, include everything
	if len(techTags) == 0 {
		return true
	}

	// Include if section matches any tech tag
	for _, tag := range techTags {
		for _, sectionTag := range section.Tags {
			if tag == sectionTag {
				return true
			}
		}
		// Also check heading directly
		if strings.Contains(h, tag) {
			return true
		}
	}

	return false
}

// inferTags extracts technology tags from section heading and content.
func inferTags(heading string, content string) []string {
	combined := strings.ToLower(heading + " " + content[:min(len(content), 500)])
	var tags []string

	tagKeywords := map[string][]string{
		"php":        {"php", "laravel", "artisan", "composer", "eloquent", "blade", "pint"},
		"vue":        {"vue", "inertia", "v-model", "v-if", "composition api"},
		"javascript": {"javascript", "js", "node", "npm", "vite", "typescript", "ts"},
		"css":        {"css", "tailwind", "scss", "sass", "less"},
		"database":   {"database", "mysql", "migration", "eloquent", "query", "sql", "db"},
		"docker":     {"docker", "container", "compose"},
		"testing":    {"test", "phpunit", "pest", "jest", "vitest"},
		"api":        {"api", "endpoint", "route", "controller", "rest"},
		"auth":       {"auth", "permission", "role", "guard", "sanctum"},
		"frontend":   {"frontend", "component", "layout", "ui", "inertia"},
		"backend":    {"backend", "controller", "middleware", "service", "job", "queue"},
	}

	for tag, keywords := range tagKeywords {
		for _, kw := range keywords {
			if strings.Contains(combined, kw) {
				tags = append(tags, tag)
				break
			}
		}
	}

	return tags
}

// extToTags maps file extension to relevant technology tags.
func extToTags(ext string) []string {
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	switch ext {
	case "php":
		return []string{"php", "backend", "database", "api"}
	case "vue":
		return []string{"vue", "javascript", "frontend", "css"}
	case "ts", "tsx", "js", "jsx":
		return []string{"javascript", "frontend"}
	case "css", "scss", "less":
		return []string{"css", "frontend"}
	case "sql":
		return []string{"database"}
	case "yml", "yaml":
		return []string{"docker"}
	case "blade.php":
		return []string{"php", "frontend"}
	default:
		return nil // no filter = include all
	}
}

// DetectDominantExt scans the top-level directories for the most common source file extension.
// Returns the extension without dot (e.g. "php", "ts", "go") or empty string.
func DetectDominantExt(dir string) string {
	counts := make(map[string]int)
	relevantExts := map[string]bool{
		".php": true, ".go": true, ".js": true, ".ts": true, ".tsx": true,
		".vue": true, ".py": true, ".rs": true, ".rb": true, ".java": true,
	}

	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if skipDirs[name] || (strings.HasPrefix(name, ".") && path != dir) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if relevantExts[ext] {
			counts[ext]++
		}
		return nil
	})

	if len(counts) == 0 {
		return ""
	}

	// Find the most common extension
	maxExt := ""
	maxCount := 0
	for ext, count := range counts {
		if count > maxCount {
			maxCount = count
			maxExt = ext
		}
	}
	return strings.TrimPrefix(maxExt, ".")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// criticalSectionKeywords identify dedicated rules sections by heading.
var criticalSectionKeywords = []string{
	"critical rules", "rules", "mandatory", "conventions",
	"coding standards", "code style", "requirements",
}

// criticalKeywords are patterns that indicate mandatory rules (fallback extraction).
var criticalKeywords = []string{
	"must", "always", "never", "required", "mandatory", "enforce",
	"every change", "after change", "before commit",
	"pint", "lint", "format", "test",
}

// isCriticalSection checks if a section heading indicates a dedicated rules section.
func isCriticalSection(heading string) bool {
	h := strings.ToLower(heading)
	for _, kw := range criticalSectionKeywords {
		if strings.Contains(h, kw) {
			return true
		}
	}
	return false
}

// ExtractCriticalRules extracts mandatory project rules from instruction files.
// Priority: dedicated "Critical Rules" / "Rules" / "Conventions" sections.
// Fallback: lines containing critical keywords (must, always, never, etc.).
// Returns a compact string (~100-300 tokens) injected into every system prompt.
func ExtractCriticalRules(dir string) string {
	files := FindAllInstructionsFiles(dir)
	if len(files) == 0 {
		return ""
	}

	var rules []string
	seen := make(map[string]bool)
	foundDedicatedSection := false

	// Pass 1: Look for dedicated rules sections (highest priority)
	for _, filePath := range files {
		pi, err := ParseInstructions(filePath)
		if err != nil {
			continue
		}
		for _, section := range pi.Sections {
			if !isCriticalSection(section.Heading) {
				continue
			}
			foundDedicatedSection = true
			lines := strings.Split(section.Content, "\n")
			for _, line := range lines {
				clean := strings.TrimSpace(line)
				clean = strings.TrimLeft(clean, "- *>0123456789.")
				clean = strings.TrimSpace(clean)
				if clean == "" || len(clean) < 5 {
					continue
				}
				if !seen[clean] {
					seen[clean] = true
					rules = append(rules, clean)
				}
			}
		}
	}

	// Pass 2: Fallback to keyword matching if no dedicated sections found
	if !foundDedicatedSection {
		for _, filePath := range files {
			pi, err := ParseInstructions(filePath)
			if err != nil {
				continue
			}
			for _, section := range pi.Sections {
				lines := strings.Split(section.Content, "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" || len(line) < 10 || len(line) > 200 {
						continue
					}
					lineLower := strings.ToLower(line)
					for _, kw := range criticalKeywords {
						if strings.Contains(lineLower, kw) {
							clean := strings.TrimLeft(line, "- *>")
							clean = strings.TrimSpace(clean)
							if clean != "" && !seen[clean] {
								seen[clean] = true
								rules = append(rules, clean)
							}
							break
						}
					}
				}
			}
		}
	}

	if len(rules) == 0 {
		return ""
	}

	// Limit to most important rules
	if len(rules) > 15 {
		rules = rules[:15]
	}

	return "<project_rules>\n" + strings.Join(rules, "\n") + "\n</project_rules>"
}

// ExtractWarmContext returns technology-filtered sections from instruction files.
// Used as "warm tier" context: injected when the AI is working with files of a specific type.
// Returns relevant sections (architecture, conventions + tech-specific) up to maxChars.
func ExtractWarmContext(dir string, fileExt string, maxChars int) string {
	if fileExt == "" || maxChars <= 0 {
		return ""
	}

	files := FindAllInstructionsFiles(dir)
	if len(files) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<project_context tech=\"" + fileExt + "\">\n")

	for _, filePath := range files {
		pi, err := ParseInstructions(filePath)
		if err != nil {
			continue
		}

		techTags := extToTags(fileExt)
		for _, section := range pi.Sections {
			// Skip sections already covered by critical rules
			if isCriticalSection(section.Heading) {
				continue
			}
			if !shouldInclude(section, techTags) {
				continue
			}
			entry := fmt.Sprintf("## %s\n%s\n\n", section.Heading, section.Content)
			if sb.Len()+len(entry) > maxChars-len("</project_context>") {
				break
			}
			sb.WriteString(entry)
		}
	}

	sb.WriteString("</project_context>")

	// Don't return if only tags and no content
	if sb.Len() < 60 {
		return ""
	}
	return sb.String()
}

// ExtractProjectStack extracts the project overview/stack section from instruction files.
// Returns a compact string with project name, tech stack, and key architectural decisions.
func ExtractProjectStack(dir string) string {
	files := FindAllInstructionsFiles(dir)
	if len(files) == 0 {
		return ""
	}

	for _, filePath := range files {
		pi, err := ParseInstructions(filePath)
		if err != nil || pi == nil {
			continue
		}
		// Find first overview section
		for _, section := range pi.Sections {
			if isOverviewSection(section.Heading) {
				content := section.Content
				// Truncate to keep it compact
				if len(content) > 500 {
					content = content[:500] + "..."
				}
				return "<project_stack>\n" + content + "\n</project_stack>"
			}
		}
	}
	return ""
}

// skipDirs are directories to skip when building project tree.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".git": true,
	".idea": true, ".vscode": true, "dist": true,
	"build": true, "storage": true, ".next": true,
	"__pycache__": true, ".cache": true, "tmp": true,
}

// GetProjectTree returns a compact directory tree of the project (max depth levels).
// Injected on first message so AI knows the project structure.
func GetProjectTree(dir string, maxDepth int) string {
	var sb strings.Builder
	sb.WriteString("<project_structure>\n")
	buildTree(&sb, dir, "", 0, maxDepth)
	sb.WriteString("</project_structure>")

	result := sb.String()
	// Limit size to avoid eating too many tokens
	const maxLen = 3000
	if len(result) > maxLen {
		result = result[:maxLen] + "\n...\n</project_structure>"
	}
	return result
}

func buildTree(sb *strings.Builder, dir string, prefix string, depth int, maxDepth int) {
	if depth >= maxDepth {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// Separate dirs and files
	var dirs []os.DirEntry
	var files []os.DirEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && depth == 0 && e.IsDir() {
			continue // skip hidden dirs at root
		}
		if e.IsDir() {
			if !skipDirs[name] {
				dirs = append(dirs, e)
			}
		} else {
			files = append(files, e)
		}
	}

	// Show files at this level (just names, no details)
	for _, f := range files {
		sb.WriteString(prefix + f.Name() + "\n")
	}

	// Recurse into dirs
	for _, d := range dirs {
		sb.WriteString(prefix + d.Name() + "/\n")
		buildTree(sb, filepath.Join(dir, d.Name()), prefix+"  ", depth+1, maxDepth)
	}
}
