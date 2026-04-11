// Copyright 2026, MITS Sp. z o.o.
// SPDX-License-Identifier: Apache-2.0

// wove-bench: Headless Wove agent for benchmarking (Terminal-Bench, SWE-bench).
// Runs the full Wove AI tool loop with filesystem and shell tools, no Electron required.
// Includes doom-loop detection, error reflection, and read-before-edit enforcement.

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/woveterm/wove/pkg/aiusechat"
	"github.com/woveterm/wove/pkg/aiusechat/chatstore"
	"github.com/woveterm/wove/pkg/aiusechat/repomap"
	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/web/sse"
)

// BenchMetrics tracks token usage and cost for benchmark reporting.
type BenchMetrics struct {
	InputTokens  int `json:"n_input_tokens"`
	OutputTokens int `json:"n_output_tokens"`
	ToolUses     int `json:"tool_uses"`
	Turns        int `json:"turns"`
	DurationMs   int `json:"duration_ms"`
	DoomLoops    int `json:"doom_loops_detected"`
}

// --- Doom Loop Detector ---
// Tracks recent tool calls and detects repetitive patterns.

type doomLoopDetector struct {
	mu          sync.Mutex
	history     []string // "toolname:argshash" entries
	maxHistory  int
	repeatLimit int // consecutive identical calls before triggering
	detected    int
}

func newDoomLoopDetector() *doomLoopDetector {
	return &doomLoopDetector{
		maxHistory:  20,
		repeatLimit: 3,
	}
}

func (d *doomLoopDetector) record(toolName string, argSummary string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	entry := toolName + ":" + argSummary
	d.history = append(d.history, entry)
	if len(d.history) > d.maxHistory {
		d.history = d.history[len(d.history)-d.maxHistory:]
	}

	// Check consecutive identical calls
	if len(d.history) >= d.repeatLimit {
		allSame := true
		last := d.history[len(d.history)-1]
		for i := 1; i <= d.repeatLimit-1; i++ {
			if d.history[len(d.history)-1-i] != last {
				allSame = false
				break
			}
		}
		if allSame {
			d.detected++
			log.Printf("[doom-loop] DETECTED: %q repeated %d times consecutively\n", last, d.repeatLimit)
			return true
		}
	}

	// Check AB pattern: [A,B,A,B,A,B]
	if len(d.history) >= 6 {
		h := d.history[len(d.history)-6:]
		if h[0] == h[2] && h[2] == h[4] && h[1] == h[3] && h[3] == h[5] && h[0] != h[1] {
			d.detected++
			log.Printf("[doom-loop] DETECTED: alternating pattern %q / %q\n", h[0], h[1])
			return true
		}
	}

	return false
}

// --- Write tracker ---
// Tracks whether the agent has written any output files.

type writeTracker struct {
	mu         sync.Mutex
	writeCount int
	lastPath   string
}

func newWriteTracker() *writeTracker {
	return &writeTracker{}
}

func (wt *writeTracker) recordWrite(path string) {
	wt.mu.Lock()
	defer wt.mu.Unlock()
	wt.writeCount++
	wt.lastPath = path
}

func (wt *writeTracker) hasWritten() bool {
	wt.mu.Lock()
	defer wt.mu.Unlock()
	return wt.writeCount > 0
}

// --- Enforcement middleware ---
// Runtime checkpoint pattern from ForgeCode: hooks around tool calls and injects
// a user-visible warning when the agent has made N+ calls without producing the
// task's expected output file. We don't parse how the agent writes — we just
// stat() the expected output path. If we can't extract an explicit path from
// the task instruction, we fall back to a broad "any new file in cwd" check.
type enforcementState struct {
	mu             sync.Mutex
	cwd            string
	startTime      time.Time
	outputPaths    []string // explicit paths mentioned in the task instruction
	toolCallCount  int
	lastNudgeCount int
	wroteAny       bool // set true once any write_file/edit_file/write-tool succeeded

	// Test-failure escalation: count distinct test-failure events seen in tool
	// outputs. After N failures we inject a "do NOT dismiss as pre-existing"
	// nudge to break confirmation bias.
	testFailureEvents int
	lastFailureNudge  int
}

// testFailurePattern matches common signals of a real test/build failure in
// tool output. Tuned to avoid noise from generic "error" mentions in docs.
var testFailurePattern = regexp.MustCompile(
	`(?m)` +
		`(?:^|[ \t])(?:` +
		`\d+\s+failed[\s,)]|` + // "1 failed", "2 failed,"
		`FAILED\s+\S|` + // "FAILED ../tests/...", pytest line format
		`FAIL(?:ED|URE)?\s*[:_]|` + // "FAILED:", "FAILURE:"
		`AssertionError\b|` +
		`compilation terminated\b|` +
		`error:.*undeclared\b|` +
		`error:.*undefined reference\b|` +
		`AttributeError:|` +
		`ImportError:|` +
		`SyntaxError:|` +
		`returned non-zero exit status [1-9]` +
		`)`)

// markWrote is called by the wrapper when a file-modifying tool succeeds.
// Once set, the middleware stops nudging (the agent is producing output).
func (es *enforcementState) markWrote() {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.wroteAny = true
}

// nudgeIfFailureDismissed scans tool result for test/build failure patterns
// and, after N independent failure events, prepends a stern warning telling
// the agent NOT to explain failures away as "pre-existing" or "unrelated".
// This catches the confirmation-bias trap where the model rationalizes a
// failed test instead of investigating the file:line.
func (es *enforcementState) nudgeIfFailureDismissed(result string) string {
	if !testFailurePattern.MatchString(result) {
		return ""
	}
	es.mu.Lock()
	es.testFailureEvents++
	count := es.testFailureEvents
	// Fire on the FIRST failure event (immediate signal), then again every 3
	// further events if the agent keeps producing failures.
	shouldFire := (count == 1) || (count >= 3 && (count-es.lastFailureNudge) >= 3)
	if shouldFire {
		es.lastFailureNudge = count
	}
	es.mu.Unlock()
	if !shouldFire {
		return ""
	}
	return fmt.Sprintf(
		"⚠️ [BENCH-VERIFY failure_event=%d] ⚠️\n"+
			"A test/build FAILURE was detected in the previous output. "+
			"Do NOT rationalize it as 'pre-existing', 'unrelated', 'minor', or 'expected'. "+
			"The benchmark verifier will check EXACTLY the failing tests. Every failure costs reward.\n"+
			"Required action:\n"+
			"  1. Read the EXACT failure location (file:line) from the error message.\n"+
			"  2. Open that file and inspect the failing code path.\n"+
			"  3. For Cython tasks: also check .pyx, .pxd, .pyi files (NOT just .py).\n"+
			"  4. For build tasks: check ALL files referenced by the failing test.\n"+
			"  5. Fix the actual bug, then re-run the same test command to confirm.\n"+
			"---\n"+
			"Tool result follows below — read it carefully for the failure details:\n\n",
		count)
}

// outputPathRegex matches absolute paths with a file extension in the
// instruction text. Covers '/app/foo.csv', '/tmp/out.txt', '/app/dir/bar.py'.
// Deliberately simple: must start with '/', contain path chars, end in .ext.
var outputPathRegex = regexp.MustCompile(`(?i)/[a-z0-9_\-./]+\.[a-z0-9]{1,8}\b`)

// extractOutputPaths scans the task instruction for explicit output file paths
// the agent is supposed to create. Deduplicates and filters obvious inputs.
func extractOutputPaths(instruction string) []string {
	matches := outputPathRegex.FindAllString(instruction, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		// Filter obvious non-output paths (inputs the agent should READ from,
		// not WRITE to). The task instruction almost always mentions both.
		lower := strings.ToLower(m)
		if strings.Contains(lower, "/tests/") ||
			strings.Contains(lower, "/verifier/") ||
			strings.HasSuffix(lower, ".md") {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func newEnforcementState(cwd string, startTime time.Time, instruction string) *enforcementState {
	paths := extractOutputPaths(instruction)
	if len(paths) > 0 {
		log.Printf("[enforce] extracted %d output path hint(s) from instruction: %v\n", len(paths), paths)
	} else {
		log.Printf("[enforce] no explicit output paths found in instruction, falling back to cwd walk\n")
	}
	return &enforcementState{
		cwd:         cwd,
		startTime:   startTime,
		outputPaths: paths,
	}
}

// hasProducedOutput returns true if the agent has written to any of the
// expected output paths (non-empty file), OR has called any write/edit tool
// at least once. If no explicit paths were extracted, falls back to walking
// cwd for any file with mtime > startTime.
func (es *enforcementState) hasProducedOutput() bool {
	es.mu.Lock()
	wrote := es.wroteAny
	es.mu.Unlock()
	if wrote {
		return true
	}

	if len(es.outputPaths) > 0 {
		for _, p := range es.outputPaths {
			info, err := os.Stat(p)
			if err == nil && !info.IsDir() && info.Size() > 0 {
				return true
			}
		}
		return false
	}

	// Fallback: broad walk.
	found := false
	skip := map[string]bool{
		".git": true, "node_modules": true, "__pycache__": true,
		".venv": true, "venv": true, ".mypy_cache": true, ".pytest_cache": true,
	}
	_ = filepath.WalkDir(es.cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(es.startTime) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// nudgeIfNoOutput returns a warning to append to a tool result if the agent
// has made many tool calls without any output file. Rate-limited: first at
// call 10, then every 5 after.
func (es *enforcementState) nudgeIfNoOutput() string {
	es.mu.Lock()
	es.toolCallCount++
	count := es.toolCallCount
	es.mu.Unlock()

	shouldCheck := (count == 10) || (count > 10 && count%5 == 0)
	if !shouldCheck {
		return ""
	}

	if es.hasProducedOutput() {
		return ""
	}

	es.mu.Lock()
	if es.lastNudgeCount == count {
		es.mu.Unlock()
		return ""
	}
	es.lastNudgeCount = count
	es.mu.Unlock()

	var pathHint string
	if len(es.outputPaths) > 0 {
		pathHint = fmt.Sprintf("Expected output: %s. ", strings.Join(es.outputPaths, ", "))
	}

	return fmt.Sprintf(
		"⚠️ [BENCH-CHECKPOINT call=%d writes=0] ⚠️\n"+
			"You have made %d tool calls and the expected output file is still empty or missing. "+
			"%s"+
			"STOP exploring and WRITE YOUR CURRENT BEST CANDIDATE NOW, "+
			"even if incomplete, wrong, or uncertain. "+
			"Empty output = 0 reward. One wrong line = still better than nothing. "+
			"After writing, continue refining — this is a checkpoint, not a stop.\n"+
			"---\n"+
			"Tool result follows below (which may be irrelevant — write the file FIRST):\n\n",
		count, count, pathHint)
}

// --- Tool error tracker ---
// Counts consecutive failures per tool, resets on success.

type toolErrorTracker struct {
	mu     sync.Mutex
	counts map[string]int
	maxErr int
}

func newToolErrorTracker(maxErr int) *toolErrorTracker {
	return &toolErrorTracker{counts: make(map[string]int), maxErr: maxErr}
}

func (t *toolErrorTracker) recordFail(tool string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[tool]++
	return t.counts[tool]
}

func (t *toolErrorTracker) recordSuccess(tool string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, tool)
}

func (t *toolErrorTracker) attemptsLeft(tool string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.maxErr - t.counts[tool]
}

// --- Read-before-edit tracker ---
// Enforces that files must be read before editing.

type readTracker struct {
	mu       sync.Mutex
	readFiles map[string]bool
}

func newReadTracker() *readTracker {
	return &readTracker{readFiles: make(map[string]bool)}
}

func (rt *readTracker) markRead(path string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.readFiles[path] = true
}

func (rt *readTracker) wasRead(path string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.readFiles[path]
}

// --- Tool call tracer ---
// Writes JSONL trace of each tool call: name, args, result (truncated), duration, success.

type toolTracer struct {
	mu   sync.Mutex
	file *os.File
	seq  int
}

func newToolTracer(path string) *toolTracer {
	if path == "" {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		log.Printf("[tracer] failed to create %s: %v\n", path, err)
		return nil
	}
	return &toolTracer{file: f}
}

func (tr *toolTracer) record(name string, args any, result string, err error, durMs int64) {
	if tr == nil {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.seq++
	truncResult := result
	if len(truncResult) > 500 {
		truncResult = truncResult[:500] + "...[truncated]"
	}
	errStr := ""
	if err != nil {
		errStr = err.Error()
		if len(errStr) > 500 {
			errStr = errStr[:500] + "...[truncated]"
		}
	}
	entry := map[string]any{
		"seq":         tr.seq,
		"t_ms":        time.Now().UnixMilli(),
		"tool":        name,
		"args":        args,
		"result":      truncResult,
		"error":       errStr,
		"duration_ms": durMs,
	}
	data, _ := json.Marshal(entry)
	tr.file.Write(data)
	tr.file.Write([]byte("\n"))
}

func (tr *toolTracer) close() {
	if tr != nil && tr.file != nil {
		tr.file.Close()
	}
}

func wrapTool(tr *toolTracer, name string, cb func(any) (string, error)) func(any) (string, error) {
	if tr == nil {
		return cb
	}
	return func(input any) (string, error) {
		start := time.Now()
		result, err := cb(input)
		tr.record(name, input, result, err, time.Since(start).Milliseconds())
		return result, err
	}
}

// --- Todo list tracker ---
// In-memory todo list. Agent replaces the full list on each write (Claude-Code semantics).

type todoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

type todoTracker struct {
	mu    sync.Mutex
	items []todoItem
}

func newTodoTracker() *todoTracker { return &todoTracker{} }

func (tt *todoTracker) set(items []todoItem) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.items = items
}

func (tt *todoTracker) render() string {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if len(tt.items) == 0 {
		return "(no todos)"
	}
	var sb strings.Builder
	sb.WriteString("<todos>\n")
	for i, t := range tt.items {
		mark := "[ ]"
		switch t.Status {
		case "in_progress":
			mark = "[~]"
		case "completed":
			mark = "[x]"
		}
		fmt.Fprintf(&sb, "%d. %s %s\n", i+1, mark, t.Content)
	}
	sb.WriteString("</todos>")
	return sb.String()
}

// --- Doom-loop alternative strategy injection ---
// When a doom loop is detected, inject tool-specific alternatives instead of a generic nudge.
func doomLoopAlternatives(tool string, lastArg string) string {
	switch tool {
	case "bash":
		return "- The command failed/looped 3x. Check if the binary is installed: `which X`.\n" +
			"- Read its docs: `X --help 2>&1 | head -40` or `man X`.\n" +
			"- Try a DIFFERENT tool entirely (python instead of awk, sed instead of python).\n" +
			"- If file paths are involved, verify they exist with `ls -la`.\n" +
			"- Wrap with `timeout 30` if it might hang."
	case "edit_file":
		return "- Your old_string never matches. Re-read the file with read_file to see CURRENT content.\n" +
			"- The file may have changed, or your old_string has wrong whitespace/indentation.\n" +
			"- Try a SHORTER, more unique old_string (one distinctive line).\n" +
			"- Or rewrite the whole file with write_file if it's small."
	case "grep":
		return "- Pattern found nothing 3x. Loosen the regex or search for a sub-string.\n" +
			"- Use `-F` for literal strings (no regex metacharacters).\n" +
			"- Search a parent directory — you may be in the wrong path.\n" +
			"- Use `list_dir` or `find` first to see what files exist."
	default:
		return "- Try a COMPLETELY different approach, not a variation of the same strategy."
	}
}

// --- First-call documentation ---
// Returns detailed usage guide on first invocation of each tool, empty string after.
type toolDocs struct {
	mu   sync.Mutex
	seen map[string]bool
	docs map[string]string
}

func newToolDocs() *toolDocs {
	return &toolDocs{
		seen: make(map[string]bool),
		docs: map[string]string{
			"bash": "[TOOL GUIDE] bash: Persistent terminal. State (cd, env, venv) preserved across calls. " +
				"Default timeout 60s — pass timeout_sec for longer ops (pip install: 180, make: 120). " +
				"NEVER apt-get install unless absolutely needed — check 'which X' first. curl/git/gcc are pre-installed. " +
				"For BINARY files (.dat, .bin, disk images, deleted-data recovery): DO NOT use bash with strings/hexdump/dd — " +
				"call the forensic_search tool instead. It's one-shot binary-safe and won't timeout.",
			"read_file": "[TOOL GUIDE] read_file: Returns file with line numbers in <file> XML tags. " +
				"Use offset/limit for large files. You MUST read a file before editing it.",
			"write_file": "[TOOL GUIDE] write_file: Overwrites entire file. Creates parent dirs. " +
				"Use edit_file for small changes instead of rewriting whole file.",
			"edit_file": "[TOOL GUIDE] edit_file: Replace exact string match. old_string must be unique in file. " +
				"You MUST read_file before editing. If old_string not found, re-read the file — content may have changed.",
			"grep": "[TOOL GUIDE] grep: Regex search in TEXT files recursively. Returns file:line format. " +
				"Use --include='*.py' to filter file types. Max 200 matches. " +
				"For BINARY files (.dat, .bin, disk images): use forensic_search tool instead — it's binary-safe and one-shot.",
			"forensic_search": "[TOOL GUIDE] forensic_search: Binary-safe recursive pattern search (wraps grep -raoE under the hood). " +
				"Use for: recovering passwords/flags/keys from deleted files, .dat/.bin files, disk images, or any non-text data. " +
				"Example: forensic_search(pattern='PASSWORD=8XD[A-Z0-9]{17}W54', path='/app/') — " +
				"returns matching strings one per line. Pattern is extended regex (ERE). " +
				"If no match: data may be fragmented/non-contiguous. Fall back to a SHORTER prefix (e.g. 'PASSWORD='), then use binary_carve at the match offsets to inspect the raw bytes around each hit.",
			"binary_carve": "[TOOL GUIDE] binary_carve: Dump a hex+ASCII window around a byte offset. " +
				"Use after forensic_search finds an anchor, or when you spot an offset in a previous hex dump. " +
				"One call replaces 'dd | xxd | grep' chains. Example: binary_carve(path='/app/file.dat', offset=1048576, radius_bytes=512) — " +
				"shows 1KB around offset 0x100000 with classic hex+ASCII format. The line containing your offset is marked with '>'. " +
				"Great for: reading partial strings in fragmented files, checking magic bytes (ZIP=50 4B, PNG=89 50 4E 47, JPEG=FF D8), " +
				"analyzing file structure without running 10 bash pipelines.",
			"todo_write": "[TOOL GUIDE] todo_write: Replaces your entire plan. Call FIRST to plan, " +
				"update after each step. Mark in_progress/completed to track progress.",
			"web_search": "[TOOL GUIDE] web_search: DuckDuckGo search. Use for docs, examples, library APIs. " +
				"Don't waste time searching after 10 minutes — focus on implementation.",
			"repo_map": "[TOOL GUIDE] repo_map: Structural overview of codebase (classes, functions, types). " +
				"Call BEFORE reading individual files to understand what exists. Much faster than reading every file.",
		},
	}
}

func (td *toolDocs) firstCallDoc(toolName string) string {
	td.mu.Lock()
	defer td.mu.Unlock()
	if td.seen[toolName] {
		return ""
	}
	td.seen[toolName] = true
	if doc, ok := td.docs[toolName]; ok {
		return doc + "\n\n"
	}
	return ""
}

// --- Error reflection wrapper ---
// Wraps tool errors with reflection prompts.

func wrapWithErrorReflection(result string, err error, toolName string, attempt int) (string, error) {
	if err == nil {
		return result, nil
	}
	attemptsLeft := 3 - attempt
	if attemptsLeft < 0 {
		attemptsLeft = 0
	}
	reflection := fmt.Sprintf(
		"%s\n\n<error_reflection_required>\nTool '%s' failed (attempt %d, %d remaining). You MUST reflect before retrying:\n1. Pinpoint EXACTLY what was wrong — wrong tool? missing parameter? malformed arguments?\n2. Explain WHY this mistake happened — did you misread the schema? miss a required field?\n3. Make the CORRECT tool call — do NOT repeat the same mistake.\n</error_reflection_required>",
		err.Error(), toolName, attempt, attemptsLeft)
	return "", fmt.Errorf("%s", reflection)
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[wove-bench] PANIC recovered: %v\n", r)
			os.Exit(1)
		}
	}()

	var (
		model      string
		apiType    string
		apiKey     string
		endpoint   string
		maxTurns   int
		timeoutSec int
		cwd        string
		systemFile string
		outputFile string
		traceFile  string
		verbose    bool
		noPty      bool
		noWeb      bool
		noRepoMap  bool
		noTodo       bool
		noLocalContext bool
		orchestrator bool
		dryRun bool
	)

	flag.StringVar(&model, "model", "MiniMax-M2.7", "AI model name")
	flag.StringVar(&apiType, "api-type", "openai-chat", "API type: openai-chat, openai-responses, anthropic-messages, google-gemini")
	flag.StringVar(&apiKey, "api-key", "", "API key (or set via WOVE_BENCH_API_KEY env)")
	flag.StringVar(&endpoint, "endpoint", "", "API endpoint URL (auto-detected from api-type if empty)")
	flag.IntVar(&maxTurns, "max-turns", 30, "Maximum agent turns")
	flag.IntVar(&timeoutSec, "timeout", 900, "Timeout in seconds")
	flag.StringVar(&cwd, "cwd", "", "Working directory for tool execution (default: current dir)")
	flag.StringVar(&systemFile, "system-prompt-file", "", "File containing additional system prompt")
	flag.StringVar(&outputFile, "output", "", "Write metrics JSON to this file")
	flag.StringVar(&traceFile, "trace", "", "Write JSONL tool-call trace to this file")
	flag.BoolVar(&verbose, "verbose", false, "Print SSE stream to stderr")
	flag.BoolVar(&noPty, "no-pty", false, "Disable persistent PTY terminal (use stateless bash)")
	flag.BoolVar(&noWeb, "no-web", false, "Disable web_search and web_fetch tools")
	flag.BoolVar(&noRepoMap, "no-repo-map", false, "Disable repo_map tool")
	flag.BoolVar(&noTodo, "no-todo", false, "Disable todo_write tool")
	flag.BoolVar(&noLocalContext, "no-local-context", false, "Disable LocalContextMiddleware (upfront ls + repo_map injection)")
	flag.BoolVar(&orchestrator, "orchestrator", false, "Enable orchestrator mode: main agent delegates to sub-agents")
	flag.BoolVar(&dryRun, "dry-run", false, "Print the initial message (instruction + context + tests) and exit without calling the model")
	flag.Parse()

	if apiKey == "" {
		apiKey = os.Getenv("WOVE_BENCH_API_KEY")
	}
	if apiKey == "" && !dryRun {
		log.Fatal("API key required: use --api-key or set WOVE_BENCH_API_KEY")
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: wove-bench [flags] <instruction>")
		os.Exit(1)
	}
	instruction := args[0]

	if dryRun {
		fmt.Println("=== SYSTEM PROMPT ===")
		fmt.Println(buildSystemPrompt(cwd))
		fmt.Println()
		fmt.Println("=== INITIAL USER MESSAGE ===")
		var sb strings.Builder
		sb.WriteString(instruction)
		if !noLocalContext {
			lc := buildLocalContext(cwd)
			if lc != "" {
				sb.WriteString("\n\n<working_directory_context>\n")
				sb.WriteString(lc)
				sb.WriteString("</working_directory_context>\n\nThe above shows the pre-scanned contents of your working directory. Use this instead of running `ls`, `find`, or `repo_map` as your first action — you already have this information.")
			}
		}
		tc := readTestFiles(cwd)
		if tc != "" {
			sb.WriteString("\n\n<test_files_content>\n")
			sb.WriteString(tc)
			sb.WriteString("\n</test_files_content>\n\nThe above shows the EXACT tests that will verify your solution. Study them carefully before implementing.")
		}
		fmt.Println(sb.String())
		fmt.Println()
		fmt.Printf("=== STATS: instruction=%d local_context=%d test_content=%d total=%d bytes ===\n",
			len(instruction), len(buildLocalContext(cwd)), len(readTestFiles(cwd)), sb.Len())
		fmt.Println()
		paths := extractOutputPaths(instruction)
		fmt.Printf("=== ENFORCEMENT: extracted %d output path hints: %v ===\n", len(paths), paths)
		return
	}

	if endpoint == "" {
		endpoint = inferEndpoint(apiType, model)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	metrics := runAgent(ctx, agentConfig{
		Model:       model,
		APIType:     apiType,
		APIKey:      apiKey,
		Endpoint:    endpoint,
		MaxTurns:    maxTurns,
		CWD:         cwd,
		Instruction: instruction,
		SystemFile:  systemFile,
		Verbose:     verbose,
		NoPty:       noPty,
		NoWeb:       noWeb,
		NoRepoMap:   noRepoMap,
		NoTodo:       noTodo,
		NoLocalContext: noLocalContext,
		TraceFile:    traceFile,
		Orchestrator: orchestrator,
	})

	metricsJSON, _ := json.MarshalIndent(metrics, "", "  ")
	if outputFile != "" {
		_ = os.WriteFile(outputFile, metricsJSON, 0644)
	}
	fmt.Fprintf(os.Stderr, "\n--- Metrics ---\n%s\n", metricsJSON)
}

func inferEndpoint(apiType, model string) string {
	switch apiType {
	case "openai-responses":
		return "https://api.openai.com/v1/responses"
	case "openai-chat":
		lower := strings.ToLower(model)
		if strings.Contains(lower, "minimax") || strings.Contains(lower, "m2.7") {
			return "https://api.minimax.io/v1/chat/completions"
		}
		return "https://api.openai.com/v1/chat/completions"
	case "anthropic-messages":
		return "https://api.anthropic.com/v1/messages"
	default:
		return "https://api.openai.com/v1/chat/completions"
	}
}

type agentConfig struct {
	Model       string
	APIType     string
	APIKey      string
	Endpoint    string
	MaxTurns    int
	CWD         string
	Instruction string
	SystemFile  string
	Verbose     bool
	NoPty       bool
	NoWeb       bool
	NoRepoMap   bool
	NoTodo       bool
	NoLocalContext bool
	TraceFile    string
	Orchestrator bool
}

func runAgent(ctx context.Context, cfg agentConfig) BenchMetrics {
	startTime := time.Now()

	doomDetector := newDoomLoopDetector()
	readFiles := newReadTracker()
	writes := newWriteTracker()
	todos := newTodoTracker()
	docs := newToolDocs()
	tracer := newToolTracer(cfg.TraceFile)
	if tracer != nil {
		defer tracer.close()
	}
	// Create persistent terminal session (unless disabled)
	var termSession *terminalSession
	if !cfg.NoPty {
		var termErr error
		termSession, termErr = newTerminalSession(cfg.CWD)
		if termErr != nil {
			log.Printf("[terminal] failed to create session: %v, falling back to stateless bash\n", termErr)
		}
		if termSession != nil {
			defer termSession.close()
		}
	}

	tools := buildStandaloneTools(cfg.CWD, doomDetector, readFiles, writes, termSession, todos, toolOpts{
		NoWeb:     cfg.NoWeb,
		NoRepoMap: cfg.NoRepoMap,
		NoTodo:    cfg.NoTodo,
	})
	// Hybrid mode: full toolset + run_sub_task for delegation (like ForgeCode Forge agent)
	tools = append(tools, uctypes.ToolDefinition{
		Name:        "run_sub_task",
		Description: "Delegate a complex sub-task to a worker agent with clean context. Worker has full tools. Use when context is large or task has independent parts.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"task"},
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "Detailed task description with ALL context. Worker has NO memory of your conversation.",
				},
				"timeout_sec": map[string]any{
					"type":        "integer",
					"description": "Max seconds (default: 300)",
				},
			},
		},
		ToolTextCallback: makeSubTaskTool(cfg, startTime),
	})

	// Wrap each tool: first-call docs + enforcement middleware + tracer
	enforce := newEnforcementState(cfg.CWD, startTime, cfg.Instruction)
	// Set of tool names that PRODUCE output (any successful call → enforce.markWrote())
	writeTools := map[string]bool{
		"write_file":  true,
		"edit_file":   true,
		"todo_write":  false, // planning, not output
	}
	for i := range tools {
		name := tools[i].Name
		origCb := tools[i].ToolTextCallback
		tools[i].ToolTextCallback = func(input any) (string, error) {
			result, err := origCb(input)
			if err == nil {
				if writeTools[name] {
					enforce.markWrote()
				}
				if doc := docs.firstCallDoc(name); doc != "" {
					result = doc + result
				}
			}
			// Enforcement middleware: after every tool call, check if expected
			// output exists. If not after N calls, PREPEND a checkpoint warning
			// to the tool result. Prepended (not appended) so the model reads
			// it FIRST — attention bias favors the start of long outputs, and
			// we observed the model ignoring trailing nudges buried after hex
			// dumps. Pure filesystem check — no parsing.
			if nudge := enforce.nudgeIfNoOutput(); nudge != "" {
				log.Printf("[enforce] checkpoint nudge injected at call %d (no output file)\n", enforce.toolCallCount)
				if err != nil {
					return result, fmt.Errorf("%s%w", nudge, err)
				}
				result = nudge + result
			}
			// Test-failure escalation: scan tool result for failure patterns
			// (pytest "X failed", AttributeError, AssertionError, build errors)
			// and prepend an anti-confirmation-bias warning. Triggers immediately
			// on first failure and again every 3 events thereafter.
			if failNudge := enforce.nudgeIfFailureDismissed(result); failNudge != "" {
				log.Printf("[enforce] failure nudge injected at call %d (test failure detected)\n", enforce.toolCallCount)
				result = failNudge + result
			}
			return result, err
		}
	}
	if tracer != nil {
		for i := range tools {
			tools[i].ToolTextCallback = wrapTool(tracer, tools[i].Name, tools[i].ToolTextCallback)
		}
	}

	systemPrompts := []string{buildSystemPrompt(cfg.CWD)}
	if cfg.SystemFile != "" {
		if data, err := os.ReadFile(cfg.SystemFile); err == nil {
			systemPrompts = append(systemPrompts, string(data))
		}
	}

	opts := &uctypes.AIOptsType{
		APIType:      cfg.APIType,
		APIToken:     cfg.APIKey,
		Endpoint:     cfg.Endpoint,
		Model:        cfg.Model,
		MaxTokens:    16384,
		Capabilities: []string{uctypes.AICapabilityTools},
	}

	chatID := uuid.New().String()
	aiMessage := &uctypes.AIMessage{
		MessageId: uuid.New().String(),
		Parts: []uctypes.AIMessagePart{
			{Type: uctypes.AIMessagePartTypeText, Text: cfg.Instruction},
		},
	}

	collector := &outputCollector{verbose: cfg.Verbose}
	sseHandler := sse.MakeSSEHandlerCh(collector, ctx)
	defer sseHandler.Close()

	chatOpts := uctypes.WaveChatOpts{
		ChatId:           chatID,
		ClientId:         uuid.New().String(),
		Config:           *opts,
		Tools:            tools,
		SystemPrompt:     systemPrompts,
		CompactThreshold: 200000, // 200KB — MiniMax M2.7 has 200K token context
		AutoApproveTools: true,   // bench: no UI approval, enables ForgeCode-style continuation
		// StepTimeoutSec: 0 = disabled. Was 300 but may cut valid long responses.
	}

	// Skip TLS verification in Docker containers that lack CA certs
	if os.Getenv("WOVE_BENCH_SKIP_TLS") == "1" || isInDocker() {
		http.DefaultTransport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		}
		log.Printf("[wove-bench] TLS verification disabled (Docker container)\n")
	}

	// Strip thinking blocks from conversation history (saves context tokens for MiniMax)
	// Keep thinking in history — MiniMax Interleaved Thinking requires it.
	// Mini-Agent docs: "CRITICAL for Interleaved Thinking to work properly!"
	// Stripping breaks chain of thought and degrades quality.
	// anthropic.StripThinkingFromHistory = false (default)
	// openaichat.StripThinkTagsFromHistory = false (default)

	// Initialize git for checkpoint/rollback support
	initGitCheckpoint(cfg.CWD)

	log.Printf("[wove-bench] model=%s api=%s endpoint=%s tools=%d\n", cfg.Model, cfg.APIType, cfg.Endpoint, len(tools))
	log.Printf("[wove-bench] cwd=%s\n", cfg.CWD)
	log.Printf("[wove-bench] instruction: %.200s\n", cfg.Instruction)
	log.Printf("[wove-bench] http: dial_timeout=30s\n")
	log.Printf("[wove-bench] sending first API request...\n")

	backend, err := aiusechat.GetBackendByAPIType(chatOpts.Config.APIType)
	if err != nil {
		log.Fatalf("[wove-bench] backend error: %v", err)
	}

	// --- LocalContextMiddleware: pre-populate cwd listing + source tree + repo_map ---
	// Inspired by LangChain's fix (top 30 → top 5 by just injecting context at start).
	// Saves the agent from spending 2-5 turns on exploration.
	var localCtx string
	if !cfg.NoLocalContext {
		localCtx = buildLocalContext(cfg.CWD)
		if localCtx != "" {
			log.Printf("[wove-bench] injected %d bytes of local context (ls + file tree + repo_map)\n", len(localCtx))
		}
	}

	// --- Test-first: pre-read tests and inject into instruction ---
	testContent := readTestFiles(cfg.CWD)
	if testContent != "" {
		log.Printf("[wove-bench] injected %d bytes of test content into instruction\n", len(testContent))
	}

	// Build combined initial message: instruction + local_context + test_files_content
	if localCtx != "" || testContent != "" {
		var fullText strings.Builder
		fullText.WriteString(cfg.Instruction)
		if localCtx != "" {
			fullText.WriteString("\n\n<working_directory_context>\n")
			fullText.WriteString(localCtx)
			fullText.WriteString("</working_directory_context>\n\nThe above shows the pre-scanned contents of your working directory. Use this instead of running `ls`, `find`, or `repo_map` as your first action — you already have this information.")
		}
		if testContent != "" {
			fullText.WriteString("\n\n<test_files_content>\n")
			fullText.WriteString(testContent)
			fullText.WriteString("\n</test_files_content>\n\nThe above shows the EXACT tests that will verify your solution. Study them carefully before implementing.")
		}
		aiMessage.Parts = []uctypes.AIMessagePart{
			{Type: uctypes.AIMessagePartTypeText, Text: fullText.String()},
		}
	}

	convertedMessage, err := backend.ConvertAIMessageToNativeChatMessage(*aiMessage)
	if err != nil {
		log.Fatalf("[wove-bench] message conversion error: %v", err)
	}
	if err := chatstore.DefaultChatStore.PostMessage(chatOpts.ChatId, &chatOpts.Config, convertedMessage); err != nil {
		log.Fatalf("[wove-bench] chatstore error: %v", err)
	}

	// Declare aiMetrics early so incremental writer can access it
	var aiMetrics *uctypes.AIMetrics

	// Write metrics incrementally so they survive if harbor kills the process
	writeMetrics := func() {
		result := BenchMetrics{
			DurationMs: int(time.Since(startTime).Milliseconds()),
			DoomLoops:  doomDetector.detected,
		}
		if aiMetrics != nil {
			result.InputTokens = aiMetrics.Usage.InputTokens
			result.OutputTokens = aiMetrics.Usage.OutputTokens
			result.ToolUses = aiMetrics.ToolUseCount
			result.Turns = aiMetrics.RequestCount
		}
		if data, err := json.Marshal(result); err == nil {
			_ = os.WriteFile("/tmp/wove-metrics.json", data, 0644)
		}
	}
	// Write initial metrics (turns=0) so adapter can read something even on kill
	writeMetrics()

	// Periodic metrics writer — every 30s to file + stderr
	metricsTicker := time.NewTicker(30 * time.Second)
	go func() {
		for range metricsTicker.C {
			writeMetrics()
			if aiMetrics != nil {
				log.Printf("[metrics] turns=%d tools=%d in=%d out=%d dur=%ds\n",
					aiMetrics.RequestCount, aiMetrics.ToolUseCount,
					aiMetrics.Usage.InputTokens, aiMetrics.Usage.OutputTokens,
					int(time.Since(startTime).Seconds()))
			}
		}
	}()

	aiMetrics, err = aiusechat.RunAIChat(ctx, sseHandler, backend, chatOpts)
	metricsTicker.Stop()
	writeMetrics() // final write
	if err != nil {
		log.Printf("[wove-bench] error: %v\n", err)
	}

	// --- Anti-early-stop ---
	// Three conditions that trigger a "continue" nudge:
	// 1. Agent stopped in <5 turns (barely tried)
	// 2. Agent never wrote any output file (nothing to verify)
	// 3. Agent used <50% of max turns and didn't verify
	maxNudges := 1 // was 3, reduced to save time for actual work
	for nudge := 0; nudge < maxNudges && aiMetrics != nil && ctx.Err() == nil; nudge++ {
		shouldNudge := false
		var nudgeMsg string

		if aiMetrics.RequestCount < 5 && aiMetrics.ToolUseCount < 6 {
			shouldNudge = true
			nudgeMsg = fmt.Sprintf("You stopped after only %d turns. The task is NOT complete.\n\n"+
				"1. Re-read the task instruction carefully\n"+
				"2. Implement the solution — create ALL required files\n"+
				"3. Verify your work\n\n"+
				"Do NOT stop until the task is fully implemented.", aiMetrics.RequestCount)
			log.Printf("[anti-stop] nudge %d: early quit (%d turns)\n", nudge+1, aiMetrics.RequestCount)
		} else if !writes.hasWritten() && aiMetrics.RequestCount < cfg.MaxTurns/2 {
			shouldNudge = true
			nudgeMsg = "You have NOT written any output files yet. The task requires you to CREATE files.\n\n" +
				"Write your solution NOW. Even a partial solution is better than nothing.\n" +
				"Do not keep reading and analyzing — write code."
			log.Printf("[anti-stop] nudge %d: no writes after %d turns\n", nudge+1, aiMetrics.RequestCount)
		} else if aiMetrics.RequestCount < cfg.MaxTurns/3 && writes.hasWritten() {
			shouldNudge = true
			nudgeMsg = fmt.Sprintf("You stopped at %d turns with >60%% budget remaining.\n\n"+
				"Verify your solution: run it, check the output, fix any issues.\n"+
				"Do not stop early when you have time to improve.", aiMetrics.RequestCount)
			log.Printf("[anti-stop] nudge %d: early stop with budget remaining (%d/%d turns)\n", nudge+1, aiMetrics.RequestCount, cfg.MaxTurns)
		}

		if !shouldNudge {
			break
		}

		retryMsg := &uctypes.AIMessage{
			MessageId: uuid.New().String(),
			Parts:     []uctypes.AIMessagePart{{Type: uctypes.AIMessagePartTypeText, Text: nudgeMsg}},
		}
		convertedRetry, retryErr := backend.ConvertAIMessageToNativeChatMessage(*retryMsg)
		if retryErr != nil {
			break
		}
		_ = chatstore.DefaultChatStore.PostMessage(chatOpts.ChatId, &chatOpts.Config, convertedRetry)
		retryMetrics, retryErr := aiusechat.RunAIChat(ctx, sseHandler, backend, chatOpts)
		if retryErr != nil {
			log.Printf("[anti-stop] retry error: %v\n", retryErr)
			break
		}
		if retryMetrics != nil {
			aiMetrics.Usage.InputTokens += retryMetrics.Usage.InputTokens
			aiMetrics.Usage.OutputTokens += retryMetrics.Usage.OutputTokens
			aiMetrics.ToolUseCount += retryMetrics.ToolUseCount
			aiMetrics.RequestCount += retryMetrics.RequestCount
		}
	}

	// --- Forced verification step ---
	// After agent finishes, inject a verification turn.
	// Adapts based on whether /tests/ exists in the container.
	if aiMetrics != nil && aiMetrics.ToolUseCount > 0 && ctx.Err() == nil {
		log.Printf("[wove-bench] injecting forced verification step\n")
		verifyMsg := &uctypes.AIMessage{
			MessageId: uuid.New().String(),
			Parts: []uctypes.AIMessagePart{
				{Type: uctypes.AIMessagePartTypeText, Text: `VERIFICATION REQUIRED before finishing.

Step 1: Check if tests exist: ls /tests/ 2>/dev/null

Step 2a: If /tests/ exists, run: bash /tests/test.sh 2>&1 || pytest /tests/ -x 2>&1
  - If tests fail, fix and re-run until they pass.

Step 2b: If /tests/ does NOT exist, verify manually:
  - Re-read the original task instruction
  - Check all required files exist at expected paths
  - Run your solution and verify the output is correct
  - Fix any issues you find

Do NOT skip verification.`},
			},
		}
		convertedVerify, verifyErr := backend.ConvertAIMessageToNativeChatMessage(*verifyMsg)
		if verifyErr == nil {
			_ = chatstore.DefaultChatStore.PostMessage(chatOpts.ChatId, &chatOpts.Config, convertedVerify)
			verifyMetrics, verifyErr := aiusechat.RunAIChat(ctx, sseHandler, backend, chatOpts)
			if verifyErr != nil {
				log.Printf("[wove-bench] verification error: %v\n", verifyErr)
			}
			if verifyMetrics != nil {
				aiMetrics.Usage.InputTokens += verifyMetrics.Usage.InputTokens
				aiMetrics.Usage.OutputTokens += verifyMetrics.Usage.OutputTokens
				aiMetrics.ToolUseCount += verifyMetrics.ToolUseCount
				aiMetrics.RequestCount += verifyMetrics.RequestCount
			}
		}
	}

	result := BenchMetrics{
		DurationMs: int(time.Since(startTime).Milliseconds()),
		DoomLoops:  doomDetector.detected,
	}
	if aiMetrics != nil {
		result.InputTokens = aiMetrics.Usage.InputTokens
		result.OutputTokens = aiMetrics.Usage.OutputTokens
		result.ToolUses = aiMetrics.ToolUseCount
		result.Turns = aiMetrics.RequestCount
	}
	return result
}

// --- Orchestrator + Worker Architecture ---

func buildOrchestratorPrompt(cwd string) string {
	return fmt.Sprintf(`## CRITICAL RULE — READ FIRST
You are an ORCHESTRATOR. ALWAYS delegate ALL work via run_sub_task.
ALWAYS use run_sub_task for ALL work: exploring, writing code, running tests, searching files.
Your ONLY job: plan with todo_write → delegate each step via run_sub_task → verify via run_sub_task.
EVERY step MUST go through run_sub_task. You have NO bash, NO file tools. Only run_sub_task and todo_write.

## Identity
You are a task orchestrator managing sub-agents to solve coding tasks.
Working directory: %s

## Your Tools (ONLY these)
- run_sub_task: Delegate work to a sub-agent with clean context. Your MAIN tool.
- todo_write: Create and update your execution plan.
- list_dir: See directory structure (quick overview only).

## Workflow (follow EXACTLY)

### Step 1: Quick Look
Run bash to see what's in the working directory: ls -la, check /tests/ existence.

### Step 2: Explore via Sub-Agent (ONE sub-task only!)
run_sub_task with a task like: "Research agent: explore %s and report a STRUCTURED summary.
Priority order:
1. TESTS FIRST: Read /tests/test*.py or /tests/test.sh — report EXACTLY what verifier checks (expected files, formats, values)
2. EXISTING CODE: Use repo_map to see all functions/classes. Read key source files fully.
3. DATA: ls -laR, file sizes, binary vs text. For binary files report: file type, size, structure hints.
4. DEPENDENCIES: Check pyproject.toml, package.json, Makefile, CMakeLists.txt — what build tools needed?

Output format — use these EXACT sections:
## Tests Expect
[what the verifier checks — files, formats, values]
## Existing Code
[functions, classes, structure]
## Files
[list with sizes and types]
## Dependencies
[build tools, packages needed]

IMPORTANT: Save your complete report to /tmp/exploration_report.txt using write_file. This file will be read by the implementation agent."

### Step 3: Plan (IMMEDIATELY after exploration)
Call todo_write with 3-5 SPECIFIC steps. Each step references exact file paths from exploration.

### Step 4: Implement (1-2 sub-tasks MAX)
run_sub_task with DETAILED description. ALWAYS include:
- "Read exploration_report.txt first for full context"
- Copy-paste ALL constraints from the original task instruction (size limits, format requirements, specific values)
- EXACT file paths to create
- Expected output format and test expectations
- Sub-agent has NO memory — if the task says "<5000 bytes" or "exactly 23 characters", you MUST include that in the sub-task description
DO NOT summarize or simplify constraints — copy them VERBATIM from the task instruction.

### Step 5: Verify (1 sub-task)
run_sub_task: "Verify: run tests, check output files exist and have correct format."

### Step 6: Fix (if needed, 1 sub-task)
If verification failed, ONE fix sub-task with the specific error message and what to change.

## Rules
- NEVER write code yourself — ALWAYS delegate via run_sub_task
- Each sub-agent gets CLEAN context — include ALL needed info in task description
- The moment a sub-agent reports success and files are written, VERIFY immediately
## TIME BUDGET (CRITICAL)
- Total: 900 seconds. You WILL be killed at 900s. Plan accordingly.
- MAX 1 exploration sub-task (60s). Then PLAN. Then IMPLEMENT.
- MAX 2-3 implementation sub-tasks (300s each).
- AFTER implementation: 1 verification sub-task (60s).
- If you spend >120s on exploration, you WILL run out of time for coding.
- ALWAYS implement by sub-task #3. If you haven't written code by sub-task #3, you're too slow.
- Write the answer/output file EARLY — even partial. Improve later.`, cwd, cwd)
}

func buildWorkerPrompt(cwd, taskDesc string) string {
	return fmt.Sprintf(`## Identity
You are a focused worker agent executing a specific sub-task.
Working directory: %s

## Your Task
%s

## CRITICAL RULES
1. FIRST: read exploration_report.txt if it exists — it contains full project analysis from the research phase.
2. WRITE OUTPUT FILES IMMEDIATELY when you have a solution. Write first, verify after.
3. A partial solution on disk beats a perfect solution in your head when time runs out.
4. For RESEARCH tasks: use repo_map first (shows all functions/classes), read tests FIRST, use structured output.
5. For CODING tasks: start coding within first 2 tool calls. Don't over-analyze.
6. You have max 300 seconds — be efficient.

## Tool Tips
- bash: State persists. Default timeout 120s.
- For BINARY files (.dat, .bin, disk images, deleted-data recovery): use forensic_search tool — one-shot binary-safe regex extraction. Do NOT use strings/hexdump/dd.
- read_file: Returns XML-tagged content with line numbers. MUST read before edit.
- edit_file: old_string must be unique. Re-read file if match fails.
- repo_map: Call first on codebases with 3+ files to see structure.
- grep: For text files. Returns file:line format.

## When done
- If this is a RESEARCH/EXPLORATION task: save your full report to /tmp/exploration_report.txt
- Summarize: what files you created/modified and key results.`, cwd, taskDesc)
}

func makeSubTaskTool(cfg agentConfig, startTime time.Time) func(any) (string, error) {
	subTaskCount := 0
	maxSubTasks := 7 // 1 explore + 1-2 implement + 1 setup + 1 test + 1 verify + 1 fix
	return func(input any) (string, error) {
		taskDesc := getStr(input, "task")
		if taskDesc == "" {
			return "", fmt.Errorf("task description is required")
		}
		subTaskCount++
		if subTaskCount > maxSubTasks {
			log.Printf("[sub-task] BLOCKED: limit %d reached (called %d times)\n", maxSubTasks, subTaskCount)
			return fmt.Sprintf("SUB-TASK LIMIT REACHED (%d/%d). You have used all your sub-tasks. If the task is done, stop. If not, the solution you have is your best attempt.", subTaskCount, maxSubTasks), nil
		}

		log.Printf("[sub-task] #%d/%d starting\n", subTaskCount, maxSubTasks)

		timeoutSec := 300
		if ts, ok := getFloat(input, "timeout_sec"); ok && ts > 0 {
			timeoutSec = int(ts)
		}

		// Time budget: don't exceed parent's remaining time minus 60s reserve
		elapsed := time.Since(startTime)
		remaining := 900*time.Second - elapsed - 60*time.Second
		if remaining < 30*time.Second {
			return "TIME_BUDGET_EXCEEDED: Less than 90 seconds remaining. Cannot start new sub-task. Write your best answer NOW.", nil
		}
		if time.Duration(timeoutSec)*time.Second > remaining {
			timeoutSec = int(remaining.Seconds())
		}

		log.Printf("[sub-task] starting: %s (timeout=%ds, elapsed=%ds)\n", taskDesc[:min(80, len(taskDesc))], timeoutSec, int(elapsed.Seconds()))

		// Create isolated context
		subCtx, subCancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
		defer subCancel()

		subChatId := uuid.New().String()
		workerPrompt := buildWorkerPrompt(cfg.CWD, taskDesc)

		// Build worker tools (full toolset)
		doom := newDoomLoopDetector()
		reads := newReadTracker()
		writes := newWriteTracker()
		todos := newTodoTracker()
		var termSession *terminalSession
		if !cfg.NoPty {
			var termErr error
			termSession, termErr = newTerminalSession(cfg.CWD)
			if termErr != nil {
				log.Printf("[sub-task] terminal failed: %v\n", termErr)
			}
			if termSession != nil {
				defer termSession.close()
			}
		}

		workerTools := buildStandaloneTools(cfg.CWD, doom, reads, writes, termSession, todos, toolOpts{
			NoWeb:     cfg.NoWeb,
			NoRepoMap: cfg.NoRepoMap,
			NoTodo:    false, // workers always get todo
		})

		// Add tracer to worker so we can extract tool results for context passing
		workerTraceFile := filepath.Join(os.TempDir(), "wove-trace-"+subChatId+".jsonl")
		workerTracer := newToolTracer(workerTraceFile)
		if workerTracer != nil {
			for i := range workerTools {
				workerTools[i].ToolTextCallback = wrapTool(workerTracer, workerTools[i].Name, workerTools[i].ToolTextCallback)
			}
		}

		// API config
		opts := &uctypes.AIOptsType{
			APIType:      cfg.APIType,
			Model:        cfg.Model,
			Endpoint:     cfg.Endpoint,
			APIToken:     cfg.APIKey,
			MaxTokens:    16384,
			Capabilities: []string{uctypes.AICapabilityTools},
		}

		workerChatOpts := uctypes.WaveChatOpts{
			ChatId:           subChatId,
			ClientId:         uuid.New().String(),
			Config:           *opts,
			Tools:            workerTools,
			SystemPrompt:     []string{workerPrompt},
			AutoApproveTools: true,
			CompactThreshold: 200000,
		}

		// Initial message: the task
		aiMessage := &uctypes.AIMessage{
			MessageId: uuid.New().String(),
			Parts:     []uctypes.AIMessagePart{{Type: uctypes.AIMessagePartTypeText, Text: taskDesc}},
		}

		backend, err := aiusechat.GetBackendByAPIType(workerChatOpts.Config.APIType)
		if err != nil {
			return "", fmt.Errorf("sub-task backend error: %v", err)
		}

		convertedMessage, err := backend.ConvertAIMessageToNativeChatMessage(*aiMessage)
		if err != nil {
			return "", fmt.Errorf("sub-task message error: %v", err)
		}
		if err := chatstore.DefaultChatStore.PostMessage(workerChatOpts.ChatId, &workerChatOpts.Config, convertedMessage); err != nil {
			return "", fmt.Errorf("sub-task chatstore error: %v", err)
		}

		// Run worker with buffered SSE handler
		recorder := httptest.NewRecorder()
		sseHandler := sse.MakeSSEHandlerCh(recorder, subCtx)

		workerMetrics, runErr := aiusechat.RunAIChat(subCtx, sseHandler, backend, workerChatOpts)
		if runErr != nil {
			log.Printf("[sub-task] error: %v\n", runErr)
		}

		// Extract result from last assistant message in chatstore
		result := "Sub-task completed."
		if workerMetrics != nil {
			result = fmt.Sprintf("Sub-task completed: %d turns, %d tool calls, %ds.",
				workerMetrics.RequestCount, workerMetrics.ToolUseCount,
				int(time.Since(startTime).Seconds()))
			log.Printf("[sub-task] done: turns=%d tools=%d in=%d out=%d\n",
				workerMetrics.RequestCount, workerMetrics.ToolUseCount,
				workerMetrics.Usage.InputTokens, workerMetrics.Usage.OutputTokens)
		}

		// Get last assistant text from chatstore
		chat := chatstore.DefaultChatStore.Get(subChatId)
		if chat != nil {
			for i := len(chat.NativeMessages) - 1; i >= 0; i-- {
				msg := chat.NativeMessages[i]
				if msg.GetRole() == "assistant" {
					// Use content size as indicator, extract via recorder output
					if msg.GetContentSize() > 0 {
						result = fmt.Sprintf("Sub-task completed (%d turns, %d tools). Check files in %s for results.",
							workerMetrics.RequestCount, workerMetrics.ToolUseCount, cfg.CWD)
					}
					break
				}
			}
		}

		// Close worker tracer to flush before reading
		if workerTracer != nil {
			workerTracer.close()
		}

		// Extract tool results from worker's trace file for rich context
		// Worker trace has all bash output, file reads etc. — the real data.
		if data, err := os.ReadFile(workerTraceFile); err == nil && len(data) > 0 {
			var toolOutputs []string
			for _, line := range strings.Split(string(data), "\n") {
				if line == "" {
					continue
				}
				var entry map[string]any
				if json.Unmarshal([]byte(line), &entry) == nil {
					toolResult, _ := entry["result"].(string)
					if len(toolResult) > 20 { // skip tiny results
						toolOutputs = append(toolOutputs, toolResult)
					}
				}
			}
			if len(toolOutputs) > 0 {
				fullResult := strings.Join(toolOutputs, "\n---\n")
				if len(fullResult) > len(result) {
					result = fullResult
				}
			}
			_ = os.Remove(workerTraceFile) // cleanup
		}

		// Truncate for orchestrator context
		if len(result) > 3000 {
			result = result[:3000] + "\n... [truncated]"
		}

		// After first sub-task (exploration), save report to file + nudge
		if subTaskCount == 1 {
			// Programmatically save exploration result to shared file
			_ = os.WriteFile(filepath.Join(cfg.CWD, "exploration_report.txt"), []byte(result), 0644)
			log.Printf("[sub-task] saved exploration report to %s/exploration_report.txt (%d bytes)\n", cfg.CWD, len(result))
			result += "\n\n--- EXPLORATION COMPLETE. Report saved to exploration_report.txt. Your next steps MUST be: 1) todo_write to plan 2) run_sub_task to IMPLEMENT code. Do NOT explore again. ---"
		}
		// No keyword-based blocker — limit of 7 sub-tasks + nudge after #1 is sufficient.
		// Previous keyword blocker was too aggressive (blocked "read exploration_report" and legit tasks).
		// Remind remaining budget
		result += fmt.Sprintf("\n[Sub-tasks used: %d/%d]", subTaskCount, maxSubTasks)

		return result, nil
	}
}

func buildSystemPrompt(cwd string) string {
	return fmt.Sprintf(`## Identity
You are Wove AI, an autonomous developer agent running in headless benchmark mode.
Be concise — lead with actions and results, not explanations.

## Environment
- Working directory: %s
- Platform: Linux (Docker container)
- Tools: bash (persistent), term_send_input, term_get_scrollback, read_file, write_file, edit_file, grep, list_dir, repo_map, web_search, web_fetch, todo_write
- Act autonomously — never ask for confirmation, never stop to ask "should I continue?"
- This conversation has unlimited context. Do NOT stop until the objective is fully achieved.
- Git is initialized for checkpointing. If your approach fails after 3 attempts, run: git checkout . to reset and try a COMPLETELY different strategy.
- NOTE: Test files at /tests/ may NOT exist during your execution. They are run AFTER you finish by an external verifier. You will NOT be able to read or run them. Focus on implementing the solution correctly based on the task instruction.

## Strategy (CRITICAL — follow this exact order)

### Phase 1: EXPLORE (0-1 turns, read-only)
You may already have a <working_directory_context> and <test_files_content> block in your first user message. If present, USE THEM — do NOT re-run list_dir, find, or repo_map. Go straight to Phase 2.

Only if those blocks are missing:
- list_dir to see what files exist
- bash: ls -la /tests/ 2>/dev/null; cat /tests/test*.py 2>/dev/null; cat /tests/test.sh 2>/dev/null
- bash: find . -type f -name "*.py" -o -name "*.c" -o -name "*.js" -o -name "*.go" -o -name "*.rs" 2>/dev/null | head -30
If project has 3+ code files, call repo_map to see structure.
If /tests/ exists, read the tests — they tell you EXACTLY what the verifier expects.

### Phase 2: PLAN (1 turn, MANDATORY — after exploration)
NOW call todo_write with 3-7 concrete steps based on what you DISCOVERED.
CRITICAL — your plan must include:
- Specific file paths you found (not "search for files" but "/app/varsea/disks/ae3f4c.dat")
- Your ANALYSIS of what you found (not "recover data" but "file is a ZIP archive containing launchcode.txt, extract with unzip or dd")
- Exact commands or approach for each step
Generic plans like "search for X" WASTE TURNS. Put your reasoning INTO the plan steps.
Update the plan AGAIN whenever you discover new facts. A good plan EVOLVES.

### Phase 3: IMPLEMENT — Start simple, iterate up (3-8 turns)
CRITICAL RULE: Start with the SIMPLEST possible solution.
- First attempt should be 1-20 lines of code that handles the core case
- Do NOT over-engineer on the first pass
- Do NOT analyze input data for 10 turns before writing code — write code after 2 turns max

### Phase 4: VERIFY your work
If /tests/ exists: run bash /tests/test.sh 2>&1 and iterate on failures.
If /tests/ does NOT exist: verify MANUALLY:
- Run your code and check the output matches what the instruction asks
- Check all required files exist at the expected paths
- Do a final sanity check: re-read the instruction, compare with what you built

### Phase 5: STUCK? — Reset and try differently
If after 3 failed attempts at the same approach:
1. Run: git checkout . (reset all changes)
2. Re-read the task instruction from scratch
3. Try a COMPLETELY different approach
Do not keep patching a broken approach. Fresh start is faster.

## Progressive Complexity
- Level 1: Hardcoded values, minimal logic
- Level 2: Basic implementation handling main case
- Level 3: Edge cases, error handling, optimizations
Start at Level 1. Only go to Level 2 if it fails. Only go to Level 3 if Level 2 fails.

## Tool Tips (READ CAREFULLY)
- bash: State persists (cd, env, venv). Default timeout 120s. Pass timeout_sec for longer ops.
- read_file: Returns XML-tagged content with line numbers. Use offset/limit for large files. MUST read before edit.
- edit_file: old_string must be unique. If not found, re-read file — content changed.
- repo_map: Call FIRST on codebases with 3+ files. Shows classes/functions/types. Faster than reading every file.
- grep: For TEXT files only. For BINARY files (.dat, .bin, disk images, deleted data recovery): use forensic_search tool — one-shot binary-safe regex extraction. NEVER use 'strings | grep' or 'hexdump | grep' on large binaries — they timeout.
- web_search: Use for docs/APIs you don't know. Don't search after 10 minutes.
- todo_write: Track your plan. Update after each step.
- run_sub_task: Delegate heavy work to a worker with clean context. Use when your context is getting large or for independent sub-problems.

Use tools in parallel when independent. Prefer edit_file over full rewrites.
- Example: repo_map("/app") → see all functions/classes, then read just the relevant ones.

## Doom Loop Prevention
If repeating the same action more than twice:
- STOP IMMEDIATELY
- Run: git checkout . to reset
- Try a COMPLETELY different approach — not a variation, a DIFFERENT strategy

## Write Early, Verify Often
- Write a plausible solution EARLY (even if incomplete). Overwrite it later as you learn more.
- Test/verify AFTER EVERY write or edit — do not batch 5 changes then test. One change → one test.
- CRITICAL: The moment you find the answer or produce output that matches requirements, WRITE IT TO THE OUTPUT FILE IMMEDIATELY. Do NOT keep searching, exploring, or verifying after you have a valid result. Write first, verify second.
- If you find a password, key, flag, or answer — write it to the required file RIGHT NOW. Every extra turn exploring AFTER finding the answer is WASTED time.

## Write-What-You-Have Rule (CRITICAL)
Whenever the task defines an output artifact (file path, format, set of required lines, CSV schema, JSON structure, etc.), the output is your SCRATCHPAD that you OVERWRITE as you learn more — NOT a final answer you hold back until certain.

Rules:
- As soon as you have ANY plausible content matching the required format, write it. Partial / approximate / uncertain — still write it.
- A wrong intermediate output costs one file-write. An empty output at end-of-task costs the whole reward.
- After writing, CONTINUE: test, verify, refine, search for better answers, and overwrite/append. Writing is not stopping.
- Rule of thumb: if you've spent 3+ tool calls analyzing data WITHOUT writing to the required output file, STOP analyzing and write your current-best candidate immediately. Then resume the search.
- If the task allows multiple candidates ("guesses allowed", "list matching X", "one per line"), APPEND each new candidate the moment it passes basic constraints. Never queue them up in your head.
- Never end a run with an empty / missing output file. An incomplete file always beats no file.

This applies to ALL task types: forensic recovery (write partial passwords), CSV generation (write the schema with zeros first, then fill), compressor output (write an empty stub then iterate), code golf (commit the working-but-long version before squeezing). Write FIRST, perfect LATER.

## Hard Constraints First
- Identify explicit hard constraints FIRST (byte limits, file counts, schema rules, compile requirements, specific formats).
- Produce the smallest artifact satisfying ALL hard constraints before doing deeper work.
- If current artifact violates a hard constraint, fix THAT before debugging or extending anything else.
- Re-check hard constraints after each meaningful change.

## Protected Files
- NEVER modify files in /tests/, /verifier/, or /.claude/ — these are owned by the benchmark.
- NEVER modify test scripts unless the task explicitly asks you to.

## Timeouts and Dependencies
- Wrap long-running commands with timeout: use "timeout 60 make" instead of bare "make".
- CRITICAL: curl, git, uv, uvx, gcc, make are ALREADY INSTALLED. Do NOT apt-get install them — check with 'which X' first.
- AVOID apt-get install at all costs — it holds dpkg lock and can hang. If you MUST install via apt, wrap with "timeout 60" and check for already-installed binaries FIRST with 'which X' or 'command -v X'.
- Prefer pip/pip3 for Python packages, npm for Node — they don't hold system locks.
- If apt-get hangs or you see "dpkg lock", KILL it immediately: pkill -9 apt-get; rm -f /var/lib/dpkg/lock-frontend.
- Bash tool default timeout is 60s per command. Pass timeout_sec parameter for longer ops.
- DO NOT pass timeout_sec < 30 unless the command is truly trivial. For cp, rm, builds, tests use 120+.
- For package installs (pip, npm, apt) use timeout_sec=180.

## Change Discipline
- Change ONE thing at a time. Test after each change. If it breaks, you know exactly what caused it.
- If 2 consecutive test runs fail with the same error, question your methodology — not just your implementation. Maybe the approach is wrong.

## Time Budget
You have MAXIMUM 15 minutes. Budget your time:
- Minutes 0-3: Understand the task (read files, check tests)
- Minutes 3-10: Implement and verify
- Minutes 10-13: Fix remaining issues
- Minutes 13-15: Final verification only — no new approaches
Do NOT use web_search after 10 minutes — it wastes time you need for implementation.
If after 15 turns you still haven't completed the task:
- Focus on getting the core requirement right
- Skip edge cases and optimizations
- A partial working solution beats a perfect unfinished one

## Unfamiliar Tools
When using an unfamiliar tool or library, read its docs first — run --help, check README.md. Never guess CLI flags.

## Error Handling
When a tool call fails:
1. Read the actual error message
2. Pinpoint what was wrong
3. Fix it — do NOT repeat the same mistake
4. After 3 failures on same operation, try different approach entirely

## Verification
NEVER consider yourself done without running tests.
If you wrote code but didn't test it, YOU ARE NOT DONE.`, cwd)
}

// buildStandaloneTools creates filesystem/shell tools with doom-loop detection and read-before-edit enforcement.
type toolOpts struct {
	NoWeb     bool
	NoRepoMap bool
	NoTodo    bool
}

func buildStandaloneTools(cwd string, doom *doomLoopDetector, reads *readTracker, writes *writeTracker, term *terminalSession, todos *todoTracker, opts toolOpts) []uctypes.ToolDefinition {
	all := []uctypes.ToolDefinition{
		{
			Name:        "bash",
			Description: "Run a bash command. State persists across calls.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"command"},
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The bash command to execute",
					},
					"timeout_sec": map[string]any{
						"type":        "integer",
						"description": "Timeout in seconds (default: 60)",
					},
				},
			},
			ToolTextCallback: makeBashTool(cwd, doom, term),
		},
		{
			Name:        "term_send_input",
			Description: "Send input to terminal for interactive programs (vim, REPL).",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"text"},
				"properties": map[string]any{
					"text": map[string]any{
						"type":        "string",
						"description": "Text to send to the terminal",
					},
					"press_enter": map[string]any{
						"type":        "boolean",
						"description": "Append newline after text (default: true)",
					},
				},
			},
			ToolTextCallback: makeTermSendInputTool(term),
		},
		{
			Name:        "term_get_scrollback",
			Description: "Read recent terminal output.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"max_bytes": map[string]any{
						"type":        "integer",
						"description": "Max bytes to return (default: 8000)",
					},
				},
			},
			ToolTextCallback: makeTermGetScrollbackTool(term),
		},
		{
			Name:        "read_file",
			Description: "Read file contents with line numbers.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path (absolute or relative to cwd)",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Starting line (0-based, default: 0)",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Max lines to read (default: 2000)",
					},
				},
			},
			ToolTextCallback: makeReadFileTool(cwd, doom, reads),
		},
		{
			Name:        "write_file",
			Description: "Write content to a file. Creates dirs if needed.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"path", "content"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to write",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Full file content",
					},
				},
			},
			ToolTextCallback: makeWriteFileTool(cwd, doom, reads, writes),
		},
		{
			Name:        "edit_file",
			Description: "Replace exact string in file. Must read_file first.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"path", "old_string", "new_string"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to edit",
					},
					"old_string": map[string]any{
						"type":        "string",
						"description": "Exact string to find (must be unique in file)",
					},
					"new_string": map[string]any{
						"type":        "string",
						"description": "Replacement string",
					},
				},
			},
			ToolTextCallback: makeEditFileTool(cwd, doom, reads),
		},
		{
			Name:        "grep",
			Description: "Search for regex pattern in files recursively.",
			InputSchema: map[string]any{
				"type": "object",
				"required": []any{"pattern"},
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regex pattern to search for",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory or file to search (default: working directory)",
					},
					"include": map[string]any{
						"type":        "string",
						"description": "File glob filter (e.g. '*.py', '*.c')",
					},
				},
			},
			ToolTextCallback: makeGrepTool(cwd, doom),
		},
		{
			Name:        "forensic_search",
			Description: "Binary-safe recursive pattern search for forensic/recovery tasks. Scans files or directories as raw bytes (works on .dat, .bin, disk images, overlay filesystems). Returns only the matched strings — ideal for recovering passwords, keys, flags, or embedded signatures from deleted or binary data. One-shot replacement for 'grep -raoE' + 'strings | grep' + 'hexdump | grep' patterns that normally timeout on large files.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"pattern"},
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Extended regex (ERE). Example recovery pattern: 'PASSWORD=8XD[A-Z0-9]{17}W54'. Use {n} for counted repeats, [A-Z0-9] for char classes, | for alternatives.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "File or directory to search. Default: working directory. Can be a single .dat/.bin file or a tree with binary files.",
					},
					"max_matches": map[string]any{
						"type":        "integer",
						"description": "Maximum matches to return (default: 100).",
					},
					"show_file": map[string]any{
						"type":        "boolean",
						"description": "Include the file path that each match came from (default: true).",
					},
				},
			},
			ToolTextCallback: makeForensicSearchTool(cwd, doom),
		},
		{
			Name:        "binary_carve",
			Description: "Dump a readable hex+ASCII window around a byte offset in a file. Much faster than chaining 'dd | xxd | grep' or 'od | sed'. Use after forensic_search finds an anchor offset, or after spotting a magic-byte signature — gives you enough context (default 256 bytes before/after) to analyze structure, find ZIP/PNG/JPEG headers, or read partial password fragments even when non-printable bytes surround them. Returns a classic xxd-style dump: offset | 16 hex bytes | ASCII gutter with '.' for non-printable chars.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the binary file (absolute or relative to cwd).",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Byte offset to center the window on. Default 0 (file start).",
					},
					"radius_bytes": map[string]any{
						"type":        "integer",
						"description": "Half-window size. Total window = 2*radius. Default 256 (512 bytes total, 32 lines). Max 8192.",
					},
				},
			},
			ToolTextCallback: makeBinaryCarveTool(cwd, doom),
		},
		{
			Name:        "list_dir",
			Description: "List files and directories.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to list (default: working directory)",
					},
				},
			},
			ToolTextCallback: makeListDirTool(cwd, doom),
		},
		{
			Name:        "repo_map",
			Description: "Structural overview of codebase: classes, functions, types.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to analyze (default: working directory)",
					},
					"max_chars": map[string]any{
						"type":        "integer",
						"description": "Maximum characters in output (default: 10000)",
					},
				},
			},
			ToolTextCallback: makeRepoMapTool(cwd),
		},
		{
			Name:        "web_search",
			Description: "Search the web for docs, examples, solutions.",
			InputSchema: map[string]any{
				"type": "object",
				"required": []any{"query"},
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
				},
			},
			ToolTextCallback: makeWebSearchTool(),
		},
		{
			Name:        "web_fetch",
			Description: "Fetch URL content as text.",
			InputSchema: map[string]any{
				"type": "object",
				"required": []any{"url"},
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "URL to fetch",
					},
				},
			},
			ToolTextCallback: makeWebFetchTool(),
		},
		{
			Name:        "todo_write",
			Description: "Plan as todo list. Call FIRST, update after each step.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"todos"},
				"properties": map[string]any{
					"todos": map[string]any{
						"type":        "array",
						"description": "Full list of todos (replaces previous list)",
						"items": map[string]any{
							"type":     "object",
							"required": []any{"content", "status"},
							"properties": map[string]any{
								"content": map[string]any{"type": "string", "description": "What to do"},
								"status":  map[string]any{"type": "string", "enum": []any{"pending", "in_progress", "completed"}},
							},
						},
					},
				},
			},
			ToolTextCallback: makeTodoWriteTool(todos),
		},
	}
	// Filter based on opts
	filtered := make([]uctypes.ToolDefinition, 0, len(all))
	for _, t := range all {
		switch t.Name {
		case "web_search", "web_fetch":
			if opts.NoWeb {
				continue
			}
		case "repo_map":
			if opts.NoRepoMap {
				continue
			}
		case "todo_write":
			if opts.NoTodo {
				continue
			}
		case "term_send_input", "term_get_scrollback":
			if term == nil {
				continue
			}
		}
		filtered = append(filtered, t)
	}
	return filtered
}

func makeTodoWriteTool(todos *todoTracker) func(any) (string, error) {
	return func(input any) (string, error) {
		m, ok := input.(map[string]any)
		if !ok {
			return "", fmt.Errorf("invalid input")
		}
		raw, ok := m["todos"].([]any)
		if !ok {
			return "", fmt.Errorf("todos must be an array")
		}
		items := make([]todoItem, 0, len(raw))
		for _, e := range raw {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			content, _ := em["content"].(string)
			status, _ := em["status"].(string)
			if content == "" {
				continue
			}
			if status == "" {
				status = "pending"
			}
			items = append(items, todoItem{Content: content, Status: status})
		}
		todos.set(items)
		log.Printf("[tool:todo_write] %d items\n", len(items))
		return todos.render(), nil
	}
}

// --- Tool implementations with doom-loop detection and error reflection ---

func resolvePath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(cwd, path))
}

func getStr(input any, key string) string {
	if m, ok := input.(map[string]any); ok {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	return ""
}

func getFloat(input any, key string) (float64, bool) {
	if m, ok := input.(map[string]any); ok {
		if v, ok := m[key].(float64); ok {
			return v, true
		}
	}
	return 0, false
}

// truncateForHash returns a short summary of args for doom-loop detection
func truncateForHash(s string) string {
	if len(s) > 100 {
		return s[:100]
	}
	return s
}

func makeBashTool(cwd string, doom *doomLoopDetector, term *terminalSession) func(any) (string, error) {
	return func(input any) (string, error) {
		command := getStr(input, "command")
		if command == "" {
			return "", fmt.Errorf("command is required")
		}

		if doom.record("bash", truncateForHash(command)) {
			rollbackCheckpoint(cwd)
			return "<DOOM_LOOP_DETECTED>You ran the same bash command 3 times. All file changes ROLLED BACK to last checkpoint. Alternatives to try:\n" + doomLoopAlternatives("bash", command) + "\nDo NOT retry the same command.</DOOM_LOOP_DETECTED>", nil
		}

		timeoutSec := 120
		if ts, ok := getFloat(input, "timeout_sec"); ok && ts > 0 {
			timeoutSec = int(ts)
		}

		log.Printf("[tool:bash] %s\n", command)

		// Try persistent terminal session first
		if term != nil {
			output, completed, err := term.runCommand(command, timeoutSec)
			if err == nil {
				output = stripANSI(output)
				wasTruncated := false
				if len(output) > 100000 {
					output = output[:50000] + "\n\n... [TRUNCATED: showing first and last 50KB of " + fmt.Sprintf("%d", len(output)) + " total bytes — use grep/tail for specific content] ...\n\n" + output[len(output)-50000:]
					wasTruncated = true
				}
				if !completed {
					output += "\n[TIMEOUT: command still running after " + fmt.Sprintf("%d", timeoutSec) + "s — use term_get_scrollback to see more output]"
				}
				if wasTruncated {
					output = "[OUTPUT TRUNCATED — see markers below]\n" + output
				}
				return output, nil
			}
			log.Printf("[tool:bash] terminal session error: %v, falling back to stateless\n", err)
		}

		// Fallback: stateless bash
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "HOME=/root", "TERM=xterm-256color")
		output, err := cmd.CombinedOutput()

		result := string(output)
		if len(result) > 100000 {
			result = result[:50000] + "\n\n... [truncated, showing first and last 50KB] ...\n\n" + result[len(result)-50000:]
		}

		if err != nil {
			return fmt.Sprintf("%s\n[exit code: %v]", result, err), nil
		}
		return result, nil
	}
}

func makeTermSendInputTool(term *terminalSession) func(any) (string, error) {
	return func(input any) (string, error) {
		text := getStr(input, "text")
		if text == "" {
			return "", fmt.Errorf("text is required")
		}
		pressEnter := true
		if m, ok := input.(map[string]any); ok {
			if v, ok := m["press_enter"].(bool); ok {
				pressEnter = v
			}
		}
		if term == nil {
			return "", fmt.Errorf("terminal session not available")
		}
		if pressEnter {
			text += "\n"
		}
		log.Printf("[tool:term_send_input] %q\n", text)
		if err := term.sendInput(text); err != nil {
			return "", err
		}
		// Wait a bit for program to react
		time.Sleep(500 * time.Millisecond)
		return "Input sent. Use term_get_scrollback to see response.", nil
	}
}

func makeTermGetScrollbackTool(term *terminalSession) func(any) (string, error) {
	return func(input any) (string, error) {
		if term == nil {
			return "", fmt.Errorf("terminal session not available")
		}
		maxBytes := 8000
		if mb, ok := getFloat(input, "max_bytes"); ok && mb > 0 {
			maxBytes = int(mb)
		}
		output := stripANSI(term.getScrollback(maxBytes))
		log.Printf("[tool:term_get_scrollback] returned %d bytes\n", len(output))
		return output, nil
	}
}

func makeReadFileTool(cwd string, doom *doomLoopDetector, reads *readTracker) func(any) (string, error) {
	return func(input any) (string, error) {
		path := getStr(input, "path")
		if path == "" {
			return "", fmt.Errorf("path is required")
		}
		fullPath := resolvePath(cwd, path)

		if doom.record("read_file", truncateForHash(fullPath)) {
			return "<doom_loop_warning>You already read this file. Use the content you have — don't re-read it.</doom_loop_warning>", nil
		}

		log.Printf("[tool:read_file] %s\n", fullPath)

		data, err := os.ReadFile(fullPath)
		if err != nil {
			_, reflErr := wrapWithErrorReflection("", err, "read_file", 1)
			return "", reflErr
		}

		reads.markRead(fullPath)

		lines := strings.Split(string(data), "\n")
		offset := 0
		limit := 2000
		if o, ok := getFloat(input, "offset"); ok {
			offset = int(o)
		}
		if l, ok := getFloat(input, "limit"); ok && l > 0 {
			limit = int(l)
		}

		if offset >= len(lines) {
			return fmt.Sprintf("(file has %d lines, offset %d is past end)", len(lines), offset), nil
		}
		end := offset + limit
		if end > len(lines) {
			end = len(lines)
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "<file path=\"%s\" lines=\"%d-%d\" total=\"%d\">\n", path, offset+1, end, len(lines))
		for i := offset; i < end; i++ {
			fmt.Fprintf(&sb, "%d\t%s\n", i+1, lines[i])
		}
		if end < len(lines) {
			fmt.Fprintf(&sb, "... [%d more lines — read_file offset=%d]\n", len(lines)-end, end)
		}
		sb.WriteString("</file>")
		return sb.String(), nil
	}
}

func makeWriteFileTool(cwd string, doom *doomLoopDetector, reads *readTracker, writes *writeTracker) func(any) (string, error) {
	return func(input any) (string, error) {
		path := getStr(input, "path")
		content := getStr(input, "content")
		if path == "" {
			return "", fmt.Errorf("path is required")
		}

		fullPath := resolvePath(cwd, path)

		doom.record("write_file", truncateForHash(fullPath))
		createCheckpoint(cwd) // save state before writing

		log.Printf("[tool:write_file] %s (%d bytes)\n", fullPath, len(content))

		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return "", fmt.Errorf("error creating directory: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("error writing file: %v", err)
		}

		// Mark as read since we just wrote it (agent knows the content)
		reads.markRead(fullPath)
		writes.recordWrite(path)

		return fmt.Sprintf("Written %d bytes to %s", len(content), path), nil
	}
}

func makeEditFileTool(cwd string, doom *doomLoopDetector, reads *readTracker) func(any) (string, error) {
	return func(input any) (string, error) {
		path := getStr(input, "path")
		oldStr := getStr(input, "old_string")
		newStr := getStr(input, "new_string")
		if path == "" || oldStr == "" {
			return "", fmt.Errorf("path and old_string are required")
		}

		fullPath := resolvePath(cwd, path)

		// Enforce read-before-edit
		if !reads.wasRead(fullPath) {
			return "", fmt.Errorf("you must read_file '%s' before editing it. Read the file first to understand its contents, then edit", path)
		}

		if doom.record("edit_file", truncateForHash(fullPath+":"+oldStr[:min(50, len(oldStr))])) {
			rollbackCheckpoint(cwd)
			return "<DOOM_LOOP_DETECTED>You tried the same edit 3 times. All changes ROLLED BACK. Alternatives:\n" + doomLoopAlternatives("edit_file", oldStr) + "</DOOM_LOOP_DETECTED>", nil
		}

		log.Printf("[tool:edit_file] %s\n", fullPath)

		data, err := os.ReadFile(fullPath)
		if err != nil {
			_, reflErr := wrapWithErrorReflection("", fmt.Errorf("error reading %s: %v", path, err), "edit_file", 1)
			return "", reflErr
		}

		content := string(data)
		count := strings.Count(content, oldStr)
		if count == 0 {
			return "", fmt.Errorf("old_string not found in %s. Re-read the file to check its current content — it may have changed since you last read it", path)
		}
		if count > 1 {
			return "", fmt.Errorf("old_string found %d times in %s — must be unique. Provide more surrounding context to make the match unique", count, path)
		}

		newContent := strings.Replace(content, oldStr, newStr, 1)
		if err := os.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
			return "", fmt.Errorf("error writing %s: %v", path, err)
		}

		return fmt.Sprintf("Edited %s: replaced 1 occurrence (%d chars → %d chars)", path, len(oldStr), len(newStr)), nil
	}
}

func makeGrepTool(cwd string, doom *doomLoopDetector) func(any) (string, error) {
	return func(input any) (string, error) {
		pattern := getStr(input, "pattern")
		if pattern == "" {
			return "", fmt.Errorf("pattern is required")
		}

		searchPath := cwd
		if p := getStr(input, "path"); p != "" {
			searchPath = resolvePath(cwd, p)
		}

		if doom.record("grep", truncateForHash(pattern+":"+searchPath)) {
			return "<DOOM_LOOP_DETECTED>Same grep pattern 3 times. Alternatives:\n" + doomLoopAlternatives("grep", pattern) + "</DOOM_LOOP_DETECTED>", nil
		}

		log.Printf("[tool:grep] %s in %s\n", pattern, searchPath)

		args := []string{"-rn", "--max-count=200", "--color=never"}
		if include := getStr(input, "include"); include != "" {
			args = append(args, "--include="+include)
		}
		args = append(args, pattern, searchPath)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "grep", args...)
		output, _ := cmd.CombinedOutput()

		result := string(output)
		if result == "" {
			return "No matches found", nil
		}
		if len(result) > 50000 {
			result = result[:50000] + "\n... [truncated]"
		}
		return result, nil
	}
}

// makeBinaryCarveTool returns a tool that dumps a hex+ASCII window around a byte
// offset. Replaces long chains of `dd | xxd | grep` with a single structured call
// that the LLM can read directly.
func makeBinaryCarveTool(cwd string, doom *doomLoopDetector) func(any) (string, error) {
	return func(input any) (string, error) {
		path := getStr(input, "path")
		if path == "" {
			return "", fmt.Errorf("path is required")
		}
		fullPath := resolvePath(cwd, path)

		var offset int64 = 0
		if o, ok := getFloat(input, "offset"); ok && o >= 0 {
			offset = int64(o)
		}
		radius := int64(256)
		if r, ok := getFloat(input, "radius_bytes"); ok && r > 0 {
			radius = int64(r)
		}
		if radius > 8192 {
			radius = 8192
		}

		if doom.record("binary_carve", fmt.Sprintf("%s:%d", fullPath, offset)) {
			return "<DOOM_LOOP_DETECTED>Same binary_carve(path,offset) called 3 times. The data at this offset is what you already saw — analyze it, don't re-fetch. If you need different bytes, change the offset or use forensic_search for a new anchor.</DOOM_LOOP_DETECTED>", nil
		}

		f, err := os.Open(fullPath)
		if err != nil {
			return "", fmt.Errorf("open failed: %w", err)
		}
		defer f.Close()

		stat, err := f.Stat()
		if err != nil {
			return "", fmt.Errorf("stat failed: %w", err)
		}
		size := stat.Size()

		// Compute window [start, end)
		start := offset - radius
		if start < 0 {
			start = 0
		}
		end := offset + radius
		if end > size {
			end = size
		}
		windowLen := end - start
		if windowLen <= 0 {
			return fmt.Sprintf("empty window: file size=%d offset=%d radius=%d", size, offset, radius), nil
		}

		buf := make([]byte, windowLen)
		if _, err := f.ReadAt(buf, start); err != nil {
			return "", fmt.Errorf("read failed at offset %d: %w", start, err)
		}

		log.Printf("[tool:binary_carve] %s offset=%d radius=%d (window %d..%d, %d bytes)\n",
			fullPath, offset, radius, start, end, windowLen)

		// xxd-style dump: 16 bytes per line
		// addr (8 hex) | 16 hex pairs (split by 8) | |ascii gutter|
		var sb strings.Builder
		fmt.Fprintf(&sb, "file: %s (size %d bytes)\n", path, size)
		fmt.Fprintf(&sb, "window: bytes [%d..%d) = %d bytes, centered on offset %d\n",
			start, end, windowLen, offset)
		if offset > 0 && offset < size {
			fmt.Fprintf(&sb, "offset marker: '>' at line containing offset %d (0x%x)\n", offset, offset)
		}
		sb.WriteString("\n")

		for lineStart := int64(0); lineStart < windowLen; lineStart += 16 {
			absAddr := start + lineStart
			lineEnd := lineStart + 16
			if lineEnd > windowLen {
				lineEnd = windowLen
			}
			// Marker if this line contains the offset
			marker := " "
			if absAddr <= offset && offset < absAddr+16 {
				marker = ">"
			}
			fmt.Fprintf(&sb, "%s%08x  ", marker, absAddr)

			// Hex bytes
			for i := int64(0); i < 16; i++ {
				if lineStart+i < lineEnd {
					fmt.Fprintf(&sb, "%02x ", buf[lineStart+i])
				} else {
					sb.WriteString("   ")
				}
				if i == 7 {
					sb.WriteString(" ")
				}
			}

			// ASCII gutter
			sb.WriteString(" |")
			for i := int64(0); i < 16; i++ {
				if lineStart+i >= lineEnd {
					sb.WriteString(" ")
					continue
				}
				c := buf[lineStart+i]
				if c >= 0x20 && c < 0x7f {
					sb.WriteByte(c)
				} else {
					sb.WriteByte('.')
				}
			}
			sb.WriteString("|\n")
		}

		result := sb.String()
		if len(result) > 60000 {
			result = result[:60000] + "\n... [truncated at 60KB — use smaller radius_bytes]"
		}
		return result, nil
	}
}

// makeForensicSearchTool wraps `grep -rEao` for binary-safe one-shot pattern extraction.
// Avoids the Python/dd/hexdump death spiral on binary forensic tasks.
func makeForensicSearchTool(cwd string, doom *doomLoopDetector) func(any) (string, error) {
	return func(input any) (string, error) {
		pattern := getStr(input, "pattern")
		if pattern == "" {
			return "", fmt.Errorf("pattern is required")
		}

		searchPath := cwd
		if p := getStr(input, "path"); p != "" {
			searchPath = resolvePath(cwd, p)
		}

		maxMatches := 100
		if m, ok := getFloat(input, "max_matches"); ok && m > 0 {
			maxMatches = int(m)
		}

		showFile := true
		if sf, ok := input.(map[string]any)["show_file"].(bool); ok {
			showFile = sf
		}

		if doom.record("forensic_search", truncateForHash(pattern+":"+searchPath)) {
			return "<DOOM_LOOP_DETECTED>Same forensic_search pattern repeated. If no matches found, check: (1) is the pattern correct? (2) try a shorter/simpler pattern fragment (e.g. just the prefix), (3) does the file exist at this path?</DOOM_LOOP_DETECTED>", nil
		}

		log.Printf("[tool:forensic_search] pattern=%q path=%q\n", pattern, searchPath)

		// grep -r (recursive) -a (binary as text) -o (only match) -E (ERE)
		// -I is NOT passed — we WANT binary files.
		// --max-count limits per-file matches to avoid floods.
		args := []string{"-raoE", fmt.Sprintf("--max-count=%d", maxMatches)}
		if showFile {
			args = append(args, "-H") // force filename even on single file
		} else {
			args = append(args, "-h") // no filename
		}
		args = append(args, pattern, searchPath)

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "grep", args...)
		output, _ := cmd.CombinedOutput()

		result := string(output)
		if result == "" {
			return fmt.Sprintf("No matches found for pattern %q in %s. Verify: (1) pattern regex is correct (try a shorter fragment first), (2) file/dir exists, (3) path is absolute.", pattern, searchPath), nil
		}

		// Trim to max_matches lines
		lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
		if len(lines) > maxMatches {
			lines = lines[:maxMatches]
			result = strings.Join(lines, "\n") + fmt.Sprintf("\n... [truncated, showing first %d matches]", maxMatches)
		} else {
			result = strings.Join(lines, "\n")
		}

		if len(result) > 20000 {
			result = result[:20000] + "\n... [output truncated at 20KB]"
		}

		return fmt.Sprintf("Found %d match(es):\n%s", len(lines), result), nil
	}
}

func makeListDirTool(cwd string, doom *doomLoopDetector) func(any) (string, error) {
	return func(input any) (string, error) {
		dirPath := cwd
		if p := getStr(input, "path"); p != "" {
			dirPath = resolvePath(cwd, p)
		}

		doom.record("list_dir", truncateForHash(dirPath))

		log.Printf("[tool:list_dir] %s\n", dirPath)

		entries, err := os.ReadDir(dirPath)
		if err != nil {
			return "", fmt.Errorf("error listing %s: %v", dirPath, err)
		}

		var sb strings.Builder
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			sb.WriteString(name + "\n")
		}
		return sb.String(), nil
	}
}

func makeRepoMapTool(cwd string) func(any) (string, error) {
	return func(input any) (string, error) {
		dirPath := cwd
		if p := getStr(input, "path"); p != "" {
			dirPath = resolvePath(cwd, p)
		}
		maxChars := 10000
		if mc, ok := getFloat(input, "max_chars"); ok && mc > 0 {
			maxChars = int(mc)
		}
		log.Printf("[tool:repo_map] %s (max %d chars)\n", dirPath, maxChars)
		result := repomap.BuildRepoMap(dirPath, maxChars)
		if result == "" || result == "<repo_map>\n</repo_map>" {
			return "No code symbols found in " + dirPath, nil
		}
		return result, nil
	}
}

// --- Web Search & Fetch ---

func makeWebSearchTool() func(any) (string, error) {
	return func(input any) (string, error) {
		query := getStr(input, "query")
		if query == "" {
			return "", fmt.Errorf("query is required")
		}
		log.Printf("[tool:web_search] %s\n", query)

		searchURL := "https://lite.duckduckgo.com/lite/?q=" + strings.ReplaceAll(query, " ", "+")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "curl", "-sL", "-A", "Mozilla/5.0", "-k", searchURL)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("search failed: %v", err)
		}

		result := stripHTMLTags(string(output))
		if len(result) > 8000 {
			result = result[:8000] + "\n... [truncated]"
		}
		if result == "" {
			return "No results found", nil
		}
		return result, nil
	}
}

func makeWebFetchTool() func(any) (string, error) {
	return func(input any) (string, error) {
		url := getStr(input, "url")
		if url == "" {
			return "", fmt.Errorf("url is required")
		}
		log.Printf("[tool:web_fetch] %s\n", url)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "curl", "-sL", "-A", "Mozilla/5.0", "--max-time", "25", "-k", url)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("fetch failed: %v", err)
		}

		result := string(output)
		if len(result) > 500 && (strings.Contains(result[:500], "<html") || strings.Contains(result[:500], "<!DOCTYPE")) {
			result = stripHTMLTags(result)
		}
		if len(result) > 15000 {
			result = result[:15000] + "\n... [truncated]"
		}
		return result, nil
	}
}

func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	inScript := false
	lastWasSpace := false
	lower := strings.ToLower(s)

	for i := 0; i < len(s); i++ {
		if i+7 < len(s) && (lower[i:i+7] == "<script" || lower[i:i+6] == "<style") {
			inScript = true
		}
		if inScript && i+9 < len(s) && (lower[i:i+9] == "</script>" || lower[i:i+8] == "</style>") {
			inScript = false
			i += 8
			continue
		}
		if inScript {
			continue
		}
		if s[i] == '<' {
			inTag = true
			continue
		}
		if s[i] == '>' {
			inTag = false
			continue
		}
		if inTag {
			continue
		}
		ch := s[i]
		if ch == '\n' || ch == '\r' || ch == '\t' {
			ch = ' '
		}
		isSpace := ch == ' '
		if isSpace && lastWasSpace {
			continue
		}
		result.WriteByte(ch)
		lastWasSpace = isSpace
	}

	text := result.String()
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#x27;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	return strings.TrimSpace(text)
}

// --- Git Checkpoint/Rollback ---

var checkpointCounter int
var checkpointMu sync.Mutex

func initGitCheckpoint(cwd string) {
	// Initialize git repo if not already one (for checkpoint/rollback)
	cmd := exec.Command("git", "init")
	cmd.Dir = cwd
	cmd.CombinedOutput() // ignore error if already initialized

	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = cwd
	cmd.CombinedOutput()

	cmd = exec.Command("git", "commit", "-m", "initial-state", "--allow-empty")
	cmd.Dir = cwd
	cmd.CombinedOutput()

	log.Printf("[checkpoint] git initialized in %s\n", cwd)
}

func createCheckpoint(cwd string) string {
	checkpointMu.Lock()
	checkpointCounter++
	name := fmt.Sprintf("cp-%d", checkpointCounter)
	checkpointMu.Unlock()

	cmd := exec.Command("bash", "-c", "git add -A && git commit -m '"+name+"' --allow-empty")
	cmd.Dir = cwd
	cmd.CombinedOutput()

	log.Printf("[checkpoint] created %s\n", name)
	return name
}

func rollbackCheckpoint(cwd string) {
	cmd := exec.Command("bash", "-c", "git checkout .")
	cmd.Dir = cwd
	output, _ := cmd.CombinedOutput()
	log.Printf("[checkpoint] rollback: %s\n", strings.TrimSpace(string(output)))
}

// buildLocalContext produces a compact upfront description of the working
// directory: top-level listing, a shallow file tree biased to source files,
// and a repo_map if the project has 3+ code files. Inspired by LangChain
// LocalContextMiddleware — gives the agent "where am I / what's here" info
// without wasting turns on discovery.
func buildLocalContext(cwd string) string {
	var sb strings.Builder

	// 1. Top-level listing (ls -la equivalent, shallow)
	entries, err := os.ReadDir(cwd)
	if err == nil {
		sb.WriteString(fmt.Sprintf("--- ls %s ---\n", cwd))
		count := 0
		for _, e := range entries {
			if count >= 40 {
				sb.WriteString(fmt.Sprintf("... (%d more entries omitted)\n", len(entries)-count))
				break
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			mark := ""
			if e.IsDir() {
				mark = "/"
			}
			sb.WriteString(fmt.Sprintf("  %-30s %8d  %s%s\n",
				e.Name(), info.Size(), e.Name(), mark))
			count++
		}
		sb.WriteString("\n")
	}

	// 2. Recursive source file tree (top N, common code extensions)
	codeExts := map[string]bool{
		".py": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true,
		".js": true, ".ts": true, ".tsx": true, ".jsx": true,
		".go": true, ".rs": true, ".rb": true, ".java": true, ".kt": true,
		".sh": true, ".bash": true, ".zsh": true,
		".r": true, ".R": true,
		".md": true, ".txt": true, ".yaml": true, ".yml": true, ".toml": true, ".json": true,
		".sql": true, ".html": true, ".css": true,
	}
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "__pycache__": true,
		".venv": true, "venv": true, "dist": true, "build": true,
		".mypy_cache": true, ".pytest_cache": true, "target": true,
	}

	var codeFiles []string
	var totalCodeFiles int
	_ = filepath.WalkDir(cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !codeExts[ext] {
			return nil
		}
		totalCodeFiles++
		if len(codeFiles) >= 60 {
			return nil
		}
		rel, relErr := filepath.Rel(cwd, path)
		if relErr != nil {
			rel = path
		}
		codeFiles = append(codeFiles, rel)
		return nil
	})

	if len(codeFiles) > 0 {
		sb.WriteString(fmt.Sprintf("--- source files (%d shown", len(codeFiles)))
		if totalCodeFiles > len(codeFiles) {
			sb.WriteString(fmt.Sprintf(", %d total", totalCodeFiles))
		}
		sb.WriteString(") ---\n")
		for _, f := range codeFiles {
			sb.WriteString("  ")
			sb.WriteString(f)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// 3. Repo map (structural symbols) — only if there's enough code
	if totalCodeFiles >= 3 {
		repoMap := repomap.BuildRepoMap(cwd, 8000)
		if repoMap != "" && repoMap != "<repo_map>\n</repo_map>" {
			sb.WriteString("--- repo_map (symbols) ---\n")
			sb.WriteString(repoMap)
			sb.WriteString("\n")
		}
	}

	result := sb.String()
	if len(result) > 12000 {
		result = result[:12000] + "\n... [truncated]"
	}
	return result
}

// readTestFiles reads test files from /tests/ and returns their content
// so the model sees test expectations in the first message (test-first pattern).
func readTestFiles(cwd string) string {
	var sb strings.Builder
	testDirs := []string{"/tests", filepath.Join(cwd, "tests")}

	for _, dir := range testDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".sh") && !strings.HasSuffix(name, ".py") && !strings.HasSuffix(name, ".bats") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			content := string(data)
			if len(content) > 5000 {
				content = content[:5000] + "\n... [truncated]"
			}
			fmt.Fprintf(&sb, "--- %s/%s ---\n%s\n\n", dir, name, content)
		}
	}

	result := sb.String()
	if len(result) > 15000 {
		result = result[:15000] + "\n... [truncated]"
	}
	return result
}

func isInDocker() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- SSE Output Collector (implements http.ResponseWriter) ---

type outputCollector struct {
	verbose bool
	header  http.Header
}

func (c *outputCollector) Header() http.Header {
	if c.header == nil {
		c.header = make(http.Header)
	}
	return c.header
}

func (c *outputCollector) Write(data []byte) (int, error) {
	if c.verbose {
		fmt.Fprintf(os.Stderr, "%s", string(data))
	}
	return len(data), nil
}

func (c *outputCollector) WriteHeader(int) {}
func (c *outputCollector) Flush()          {}

func (c *outputCollector) SetWriteDeadline(time.Time) error { return nil }
func (c *outputCollector) SetReadDeadline(time.Time) error  { return nil }
