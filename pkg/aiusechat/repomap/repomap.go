// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

// Package repomap generates a compact structural map of a code repository
// by extracting class, function, method, and type definitions using tree-sitter.
// The output is designed to be injected into AI system prompts for architectural awareness.
package repomap

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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

	// cached after first use
	initOnce    sync.Once
	cachedLang  *ts.Language
	cachedQuery *ts.Query
}

// getParserAndQuery returns cached language and query, initializing on first call.
func (lc *langConfig) getParserAndQuery() (*ts.Language, *ts.Query) {
	lc.initOnce.Do(func() {
		lc.cachedLang = lc.langFn()
		if lc.cachedLang != nil {
			q, err := ts.NewQuery(lc.query, lc.cachedLang)
			if err == nil {
				lc.cachedQuery = q
			}
		}
	})
	return lc.cachedLang, lc.cachedQuery
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
		langFn: grammars.TypescriptLanguage,
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
	"tests": true, "test": true, "spec": true,
	"public": true, "resources": true, "database": true,
	"bootstrap": true, "config": true, "lang": true,
	"stubs": true, ".output": true, "coverage": true,
}

const (
	maxFileSize  = 256 * 1024 // largest file to parse
	maxTotalChars = 6000      // output size limit
	maxFiles     = 150        // max files to parse
	numWorkers   = 4          // concurrent parsers
)

// cache holds the last repo map result to avoid re-parsing.
var (
	cacheMu     sync.Mutex
	cachedRoot  string
	cachedMap   string
	cachedTime  time.Time
	cacheTTL    = 5 * time.Minute
)

// BuildRepoMap walks a directory and produces a structural map of all definitions.
// Results are cached for 5 minutes per directory.
func BuildRepoMap(root string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = maxTotalChars
	}

	// Check cache
	cacheMu.Lock()
	if cachedRoot == root && cachedMap != "" && time.Since(cachedTime) < cacheTTL {
		result := cachedMap
		cacheMu.Unlock()
		return result
	}
	cacheMu.Unlock()

	start := time.Now()

	// Collect files to parse
	type fileJob struct {
		path    string
		relPath string
		cfg     *langConfig
	}

	var jobs []fileJob
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
		if len(jobs) >= maxFiles {
			return filepath.SkipAll
		}

		ext := filepath.Ext(path)
		if strings.HasSuffix(path, ".blade.php") {
			return nil
		}

		cfg, ok := langConfigs[ext]
		if !ok {
			return nil
		}

		relPath, _ := filepath.Rel(root, path)
		jobs = append(jobs, fileJob{path: path, relPath: relPath, cfg: cfg})
		return nil
	})

	log.Printf("[repomap] found %d files to parse in %s\n", len(jobs), root)

	// Parse files concurrently
	type result struct {
		idx     int
		symbols FileSymbols
	}

	results := make([]result, 0, len(jobs))
	resultCh := make(chan result, len(jobs))
	jobCh := make(chan int, len(jobs))

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobCh {
				job := jobs[idx]
				symbols := extractSymbols(job.path, job.cfg)
				if len(symbols) > 0 {
					resultCh <- result{idx: idx, symbols: FileSymbols{RelPath: job.relPath, Symbols: symbols}}
				}
			}
		}()
	}

	for i := range jobs {
		jobCh <- i
	}
	close(jobCh)
	wg.Wait()
	close(resultCh)

	for r := range resultCh {
		results = append(results, r)
	}

	// Sort by original order (path-based)
	sort.Slice(results, func(i, j int) bool {
		return results[i].symbols.RelPath < results[j].symbols.RelPath
	})

	allFiles := make([]FileSymbols, len(results))
	for i, r := range results {
		allFiles[i] = r.symbols
	}

	mapStr := formatRepoMap(allFiles, maxChars)

	log.Printf("[repomap] parsed %d/%d files in %.1fs\n", len(results), len(jobs), time.Since(start).Seconds())

	// Update cache
	cacheMu.Lock()
	cachedRoot = root
	cachedMap = mapStr
	cachedTime = time.Now()
	cacheMu.Unlock()

	return mapStr
}

// extractSymbols parses a file and extracts all definition symbols.
// Uses pre-compiled parsers and queries for performance.
func extractSymbols(filePath string, cfg *langConfig) []Symbol {
	source, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}

	lang, query := cfg.getParserAndQuery()
	if lang == nil || query == nil {
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

// FilterByKind filters a repo map string to only include lines containing definitions of a specific kind.
func FilterByKind(repoMap string, kind string) string {
	if repoMap == "" || kind == "" {
		return repoMap
	}
	prefix := kind + " "
	var sb strings.Builder
	sb.WriteString("<repo_map>\n")
	lines := strings.Split(repoMap, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "<repo_map>") || strings.HasPrefix(line, "</repo_map>") || strings.HasPrefix(line, "... [truncated]") {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Parse "path: kind1 name1, kind2 name2, ..."
		colonIdx := strings.Index(line, ": ")
		if colonIdx < 0 {
			continue
		}
		filePath := line[:colonIdx]
		defsStr := line[colonIdx+2:]
		defs := strings.Split(defsStr, ", ")
		var matched []string
		for _, def := range defs {
			if strings.HasPrefix(def, prefix) {
				matched = append(matched, def)
			}
		}
		if len(matched) > 0 {
			sb.WriteString(filePath + ": " + strings.Join(matched, ", ") + "\n")
		}
	}
	sb.WriteString("</repo_map>")
	result := sb.String()
	if result == "<repo_map>\n</repo_map>" {
		return ""
	}
	return result
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
