// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

// Package repomap generates a compact structural map of a code repository
// by extracting class, function, method, and type definitions using tree-sitter.
// The output is designed to be injected into AI system prompts for architectural awareness.
package repomap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// Symbol represents a code definition extracted from a source file.
type Symbol struct {
	Name string
	Kind string // "func", "method", "class", "type", "interface", "enum"
	Line int
}

// FileSymbols holds all definitions for a single file.
type FileSymbols struct {
	RelPath string
	Symbols []Symbol
}

// langConfig holds the tree-sitter language and query for extracting definitions.
type langConfig struct {
	langFn func() *ts.Language
	query  string
}

// langConfigs maps file extensions to their tree-sitter configuration.
var langConfigs = map[string]*langConfig{
	".go": {
		langFn: grammars.GoLanguage,
		query: `
(function_declaration name: (identifier) @name.def.func) @def.func
(method_declaration name: (field_identifier) @name.def.method) @def.method
(type_spec name: (type_identifier) @name.def.type) @def.type
`,
	},
	".php": {
		langFn: grammars.PhpLanguage,
		query: `
(class_declaration name: (name) @name.def.class) @def.class
(interface_declaration name: (name) @name.def.interface) @def.interface
(trait_declaration name: (name) @name.def.class) @def.class
(function_definition name: (name) @name.def.func) @def.func
(method_declaration name: (name) @name.def.method) @def.method
`,
	},
	".js": {
		langFn: grammars.JavascriptLanguage,
		query: `
(function_declaration name: (identifier) @name.def.func) @def.func
(class_declaration name: (identifier) @name.def.class) @def.class
(method_definition name: (property_identifier) @name.def.method) @def.method
`,
	},
	".ts": {
		langFn: grammars.TypescriptLanguage,
		query: `
(function_declaration name: (identifier) @name.def.func) @def.func
(class_declaration name: (type_identifier) @name.def.class) @def.class
(interface_declaration name: (type_identifier) @name.def.interface) @def.interface
(type_alias_declaration name: (type_identifier) @name.def.type) @def.type
(enum_declaration name: (identifier) @name.def.enum) @def.enum
(method_definition name: (property_identifier) @name.def.method) @def.method
`,
	},
	".tsx": {
		langFn: grammars.TypescriptLanguage, // TSX uses same grammar in gotreesitter
		query: `
(function_declaration name: (identifier) @name.def.func) @def.func
(class_declaration name: (type_identifier) @name.def.class) @def.class
(interface_declaration name: (type_identifier) @name.def.interface) @def.interface
(type_alias_declaration name: (type_identifier) @name.def.type) @def.type
(method_definition name: (property_identifier) @name.def.method) @def.method
`,
	},
	".py": {
		langFn: grammars.PythonLanguage,
		query: `
(class_definition name: (identifier) @name.def.class) @def.class
(function_definition name: (identifier) @name.def.func) @def.func
`,
	},
	".rs": {
		langFn: grammars.RustLanguage,
		query: `
(function_item name: (identifier) @name.def.func) @def.func
(struct_item name: (type_identifier) @name.def.type) @def.type
(enum_item name: (type_identifier) @name.def.enum) @def.enum
(trait_item name: (type_identifier) @name.def.interface) @def.interface
(impl_item type: (type_identifier) @name.def.type) @def.type
`,
	},
	".vue": {
		langFn: grammars.VueLanguage,
		query: `
(script_element (raw_text) @script)
`,
	},
}

// skipDirs are directories to skip when scanning.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".git": true,
	".idea": true, ".vscode": true, "dist": true,
	"build": true, "storage": true, ".next": true,
	"__pycache__": true, ".cache": true, "tmp": true,
	".claude": true, ".kilocode": true, ".roo": true,
}

// maxFileSize is the largest file we'll attempt to parse (256KB).
const maxFileSize = 256 * 1024

// maxTotalChars limits the output size to prevent eating too many tokens.
const maxTotalChars = 6000

// BuildRepoMap walks a directory and produces a structural map of all definitions.
// The result is a compact string suitable for injection into an AI system prompt.
func BuildRepoMap(root string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = maxTotalChars
	}

	var allFiles []FileSymbols
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			if skipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > maxFileSize || info.Size() == 0 {
			return nil
		}

		ext := filepath.Ext(path)
		// Handle .blade.php
		if strings.HasSuffix(path, ".blade.php") {
			return nil // skip blade templates
		}

		cfg, ok := langConfigs[ext]
		if !ok {
			return nil
		}

		relPath, _ := filepath.Rel(root, path)
		symbols := extractSymbols(path, cfg)
		if len(symbols) > 0 {
			allFiles = append(allFiles, FileSymbols{
				RelPath: relPath,
				Symbols: symbols,
			})
		}
		return nil
	})

	// Sort by path for consistent output
	sort.Slice(allFiles, func(i, j int) bool {
		return allFiles[i].RelPath < allFiles[j].RelPath
	})

	return formatRepoMap(allFiles, maxChars)
}

// extractSymbols parses a file and extracts all definition symbols.
func extractSymbols(filePath string, cfg *langConfig) []Symbol {
	source, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}

	lang := cfg.langFn()
	if lang == nil {
		return nil
	}

	parser := ts.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil || tree == nil {
		return nil
	}

	root := tree.RootNode()
	if root == nil {
		return nil
	}

	query, err := ts.NewQuery(cfg.query, lang)
	if err != nil {
		return nil
	}

	matches := query.ExecuteNode(root, lang, source)

	var symbols []Symbol
	seen := make(map[string]bool)

	for _, match := range matches {
		for _, capture := range match.Captures {
			if !strings.HasPrefix(capture.Name, "name.def.") {
				continue
			}
			kind := strings.TrimPrefix(capture.Name, "name.def.")
			name := string(source[capture.Node.StartByte():capture.Node.EndByte()])
			line := int(capture.Node.StartPoint().Row) + 1

			key := fmt.Sprintf("%s:%s:%d", kind, name, line)
			if seen[key] {
				continue
			}
			seen[key] = true

			symbols = append(symbols, Symbol{
				Name: name,
				Kind: kind,
				Line: line,
			})
		}
	}

	return symbols
}

// formatRepoMap formats extracted symbols into a compact string.
func formatRepoMap(files []FileSymbols, maxChars int) string {
	if len(files) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<repo_map>\n")

	for _, f := range files {
		line := f.RelPath + ": "
		var parts []string
		for _, sym := range f.Symbols {
			parts = append(parts, sym.Kind+" "+sym.Name)
		}
		line += strings.Join(parts, ", ")
		line += "\n"

		if sb.Len()+len(line) > maxChars-len("</repo_map>") {
			sb.WriteString("... [truncated]\n")
			break
		}
		sb.WriteString(line)
	}

	sb.WriteString("</repo_map>")
	return sb.String()
}
