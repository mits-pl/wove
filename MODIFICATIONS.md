# Wove — Modifications from Wave Terminal

Wove is built on the [Wave Terminal](https://github.com/wavetermdev/waveterm) engine (Apache 2.0).
This document lists all modifications and additions made in Wove.

## MCP (Model Context Protocol) Integration
- Full MCP client package (`pkg/mcpclient/`) — JSON-RPC 2.0 over stdio
- Auto-detect `.mcp.json` in terminal CWD with connect banner
- MCP tools registered as AI tools — model queries database, searches docs, reads logs
- MCP auto-context injection (database schema, application info)
- MCP Client widget in sidebar with tools list, call log, and Run button
- MCP Context toggle in AI panel header

## AI Planning System
- Multi-step execution plans with `wave_utils(action='plan_create')`
- Auto-append steps: lint, review against project conventions, write tests, run tests
- Live progress panel with expandable step results
- Plans persist to disk, survive restarts
- Detailed plan steps with file paths, conventions, and acceptance criteria

## Project Intelligence
- Reads WAVE.md, CLAUDE.md, .cursorrules, AGENTS.md automatically
- Project stack injection (tech stack in every request)
- Critical rules auto-extraction (must/always/never patterns)
- Project tree on first message (directory structure)
- Two-step project_instructions tool (table of contents → specific sections)
- Smart filtering by technology (PHP sections for .php files, etc.)

## Web Content Tools
- `web_read_text` — extract clean text by CSS selector
- `web_read_html` — extract innerHTML by CSS selector
- `web_seo_audit` — full SEO audit (JSON-LD, OG, meta, headings, alt text, links)
- `execJs` option for arbitrary JavaScript execution in webview
- Auto-refresh page before reading content
- AI Reading highlight animation on matched elements
- Page title tracking in block metadata

## Web Capture (CDP)
- `web_capture` — CDP-based visual snapshot of webview for LLM vision
- `DOMSnapshot.captureSnapshot` for DOM tree with global coordinates (including iframes)
- `Page.captureScreenshot` for JPEG screenshot (768px wide, quality 50, ~30-60 KB)
- Set-of-Mark (SoM) numbered markers injected before screenshot for element identification
- Structured element list with tag, text, bbox, interactive flag, and CSS selector
- Human-readable descriptions: `[Click] button#submit "Sign Up" at (201,450)`
- CSS selectors ready to use with `web_click` / `web_type_input`
- Multi-part tool result: text (element list) + image (screenshot) sent together to LLM
- `ToolImageTextCallback` — new callback type for tools returning text + image
- Anthropic, OpenAI Responses, and Gemini backends updated for multi-part tool results
- Cap at 200 elements, interactive elements prioritized

## Web Automation Tools
- `web_click` — click element by CSS selector (follows links, dispatches click events)
- `web_mouse_click` — CDP-based `Input.dispatchMouseEvent` for reliable clicks inside iframes (replaced Electron `sendInputEvent`)
- `web_type_input` — type text into input/textarea/contenteditable with framework event dispatch
- `web_press_key` — simulate keydown/keypress/keyup events (Enter, Tab, Escape, arrows, etc.)
- `web_exec_js` — execute arbitrary JavaScript in webview context (preserves page state)
- `web_open` — open new web browser widget with URL (registers widget ownership)
- `web_navigate` — navigate existing web widget to URL
- `close_widget` — close AI-created widgets with ownership enforcement (cannot close user's pre-existing widgets)

## Sub-task System
- `run_sub_task` tool — spawns isolated AI conversation in a new tab
- Prevents context window overflow on complex multi-step tasks (audits, migrations)
- Sub-task gets fresh context with access to all tools (terminal, web browser, etc.)
- Results saved to file; only summary returned to parent
- Nesting depth limit (max 2 levels)
- `SubTaskUpdateData` event for real-time status updates
- Auto-approve tools in sub-tasks (skip UI approval)

## Tool Result Compaction
- Automatic truncation of old tool results to prevent context overflow
- Keeps 4 most recent tool results at full length
- Older results truncated to 500 characters with `[truncated]` marker
- `CompactToolResult` / `IsToolResultMessage` implemented across all backends (Anthropic, OpenAI, Gemini, OpenAI-chat)
- `CompactOldToolResults` in ChatStore for centralized compaction

## Widget Ownership Tracking
- `OwnedWidgetSet` — thread-safe tracker for AI-created widgets
- `web_open` registers created widgets in ownership set
- `close_widget` tool with ownership enforcement
- AI cannot close user's pre-existing widgets, only widgets it created
- Supports auto-cleanup when sub-task finishes

## Skills System
- `invoke_skill` tool — replaces system prompt injection with on-demand skill loading
- Slash command autocomplete dropdown in AI input with keyboard navigation (Arrow, Tab, Enter, Escape)
- `/wave/ai/skills` HTTP endpoint for frontend to fetch skill manifests
- Lazy skill fetching — only loaded when user types `/`
- `SkillManifest` type with name, description, allowed tools, argument hints
- Enhanced skill instructions: autonomous execution, no unnecessary confirmation prompts

## Owner Profile
- `get_owner_profile` tool — reads personal info from `~/.waveterm/owner.md`
- Used for checkout, form filling, and tasks requiring personal information
- Guides user to create profile if not found

## Session History
- Chat history saved per tab at shutdown
- Previous Session banner in AI panel
- `session_history` tool for AI to read previous work
- Chat-to-tab mapping registry

## Auto-approve File Reading
- Session-level auto-approve for directories
- Sensitive path protection (~/.ssh, ~/.aws, .env)
- Symlink bypass prevention via canonical path resolution

## AI Model Management
- Quick Add Model menu (Claude, GPT, Gemini, MiniMax, Ollama, OpenRouter)
- Inline API key input with secure storage
- 10 built-in BYOK presets with endpoints
- Secret-based preset filtering (hide unconfigured models)
- Ollama connectivity check

## Repo Map (Tree-Sitter)
- `repomap` package using `gotreesitter` (pure Go, no CGO) for structural codebase awareness
- Extracts class/function/method/type definitions from source files
- Supports Go, PHP, JS, TS, TSX, Python, Rust, Vue
- Injected into system prompt on first message — AI knows what's where without exploratory tool calls
- Cached for 5 minutes per directory (instant on subsequent messages)
- Pre-compiled tree-sitter queries per extension (`sync.Once`)
- Concurrent file parsing (4 workers), max 150 files, 15-second timeout with fallback to directory tree

## Three-Tier Context Architecture
- **Critical rules**: Prioritizes dedicated `## Rules` / `## Critical Rules` sections from WAVE.md before keyword matching (`must`/`always`/`never`)
- **Warm context**: Technology-filtered conventions from instruction files based on project's dominant language
- `DetectDominantExt` scans project to find the most common source file extension for filtering
- Warm context injected on first message only (not every message)

## Read-Before-Write Enforcement
- `ReadTracker` module tracks files read during AI session
- `edit_text_file` and `write_text_file` fail if file wasn't read first (prevents blind overwrites)
- New files exempt from write check
- `term_run_command` output enriched with `cwd` and `git_branch` for context
- Context overflow warning injected after 8+ API requests, suggesting sub-tasks

## Token Usage Display
- `stream_options.include_usage` sent in OpenAI-compatible chat completions requests
- Usage parsed from final stream chunk (input/output/total tokens)
- `data-usage` SSE event sent to frontend after each AI step
- Token count rendered under AI messages in gray text

## Session Write Auto-Approve
- `AddSessionWriteApproval()` / `IsSessionWriteApproved()` — session-level directory approval for file writes
- `write_text_file` and `edit_text_file` check write approval before requiring manual confirm
- Sensitive path protection (same as read: `~/.ssh`, `~/.aws`, etc.)
- "Allow writing in this session" button in AI tool approval UI
- New RPC command `WaveAISessionWriteApproveCommand`

## System Prompt Optimization
- "Senior software engineer" role for better code quality
- "Read sibling files before writing" pattern matching
- Self-review after each plan step
- Compressed tool descriptions (~60% fewer tokens)
- Consolidated wave_utils multi-action tool
- English-only code comments enforcement
- Terminal commands reference (grep, find, php -l, pint)
- Web browsing guidance — always use webview widget for search (user can follow along)
- Web interaction — always `web_capture` before clicking for element selectors
- Sub-task guidance — use `run_sub_task` for 3+ independent steps
- Cleanup instructions — close terminals and browsers after finishing
- Autonomous execution — never ask "should I continue?" during multi-step tasks

## Terminal Tool Improvements
- `term_run_command` — event-driven output via `WatchRTInfoShellState` channel (replaced 250ms polling)
- `term_send_input` — `press_enter` parameter for auto-appending carriage return
- `term_send_input` — allow empty text (for sending just Enter)
- `term_run_command` — show error when output cannot be read instead of empty result
- xterm.js write buffer flush before reading terminal content (ensures latest data)

## Claude Code Integration
- `wsh setrtinfo` command for setting runtime info fields from shell hooks
- `claude:state` RTInfo field — Claude Code hooks notify Wove AI when Claude is idle/working
- Event-driven RTInfo updates via `WatchRTInfoShellState` pub/sub channel

## UI Enhancements
- AI tool status display — thinking indicator shows which tool is running and completed count
- Block header CSS wrapping for URL bar in web widgets
- `web_capture` null safety — skip nodes with missing `nodeName` in CDP snapshots

## Quality & Reliability
- Syntax highlighting fix in AI diff viewer (preserved file extensions)
- Language detection from filename (30+ extensions)
- New file diff: empty original, green additions
- Web page title in tab state (catches 500 errors)
- Default AI timeout: 90 seconds (was infinite)
- Default max output tokens: 16K (was 4K)
- Friendly error messages with Retry button
- MCP client: mutex protection, read timeout, graceful shutdown
- RPC handler input validation for WebSelector opts
- SSE write deadline reset made non-fatal (httptest compatibility)

## Based On
- [Wave Terminal](https://github.com/wavetermdev/waveterm) by Command Line Inc.
- Licensed under Apache License 2.0
