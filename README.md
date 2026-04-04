<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/wove-dark.png">
    <source media="(prefers-color-scheme: light)" srcset="./assets/wove-light.png">
    <img alt="Wove" src="./assets/wove-light.png" width="240">
  </picture>
</p>

# Wove

**Open-source AI coding agent. Desktop app. Bring your own model.**

AI coding tools today lock you into one model, one vendor, one monthly subscription. Your code goes through someone else's servers. You can't switch models when a cheaper one fits. You can't work offline. And you can't see what the tool actually does with your code.

Wove fixes this. It's a desktop app that connects directly to any AI provider with your own API key. It writes code, runs commands, browses the web, and verifies its own work — all locally on your machine. No proxy, no cloud agent, no vendor lock-in.

Use Claude, GPT, Gemini, MiniMax, Ollama, or any OpenAI-compatible model. Switch between them in one click. Pay only for what you use.

Built on [Wave Terminal](https://github.com/wavetermdev/waveterm) (Go + Electron). Apache 2.0.

## Download

**macOS (Apple Silicon / M1+):**
[Wove-darwin-arm64-0.2.0.dmg](https://github.com/mits-pl/wove/releases/download/v0.2.0/Wove-darwin-arm64-0.2.0.dmg) ·
[zip](https://github.com/mits-pl/wove/releases/download/v0.2.0/Wove-darwin-arm64-0.2.0.zip)

**macOS (Intel):**
[Wove-darwin-x64-0.2.0.dmg](https://github.com/mits-pl/wove/releases/download/v0.2.0/Wove-darwin-x64-0.2.0.dmg) ·
[zip](https://github.com/mits-pl/wove/releases/download/v0.2.0/Wove-darwin-x64-0.2.0.zip)

> Linux and Windows builds coming soon.

[All releases](https://github.com/mits-pl/wove/releases)

## Why Wove

**You own the whole stack.** Wove is a desktop app that talks directly to AI providers. Your code stays on your machine. Your API keys stay in an encrypted local keystore. There's no proxy, no telemetry requirement, no usage cap.

**It actually does the work.** Wove doesn't just suggest code — it reads your project, writes files, runs tests, opens a browser to check the result, and fixes what's broken. When a task is too big, it splits it into sub-tasks with isolated context so nothing overflows.

**Any model, one interface.** Claude Opus for hard problems. GPT-5 Mini for quick tasks. MiniMax M2.7 for $3/month coding. Ollama for fully offline work. Switch in one click, same tools and guardrails everywhere.

## What it does

### Writes and edits code
Reads your project conventions (WAVE.md, CLAUDE.md), finds similar files for style reference, writes code that matches your codebase. Enforces read-before-edit at tool level — the AI physically cannot modify a file it hasn't read.

### Runs and verifies
Executes commands in a real terminal. Runs your test suite. Checks linter output. Won't say "done" without running at least one verification step. If large edits are needed, breaks them into stages instead of rewriting entire files.

### Browses the web
Built-in browser with CDP-based vision. The agent opens pages, reads content, clicks buttons, fills forms, takes screenshots with numbered element markers. Useful for testing web apps, scraping docs, or debugging frontends.

### Plans and delegates
Creates execution plans with concrete file paths, tracks progress in a live panel. When tasks are complex (5+ files, multi-step migrations), automatically delegates to sub-tasks running in isolated tabs with fresh context.

### Connects to your data
Auto-detects MCP servers from `.mcp.json`. The AI gets direct access to your database schema, documentation, logs — any MCP-compatible data source.

## Models

| Provider | Models | Get API key |
|----------|--------|-------------|
| **Anthropic** | Claude Sonnet 4.6, Opus 4.6 | [console.anthropic.com](https://console.anthropic.com/) |
| **OpenAI** | GPT-5 Mini, GPT-5.1 | [platform.openai.com](https://platform.openai.com/) |
| **Google** | Gemini 3.0 Flash, Pro | [aistudio.google.com](https://aistudio.google.com/) |
| **MiniMax** | M2.7, M2.7 High Speed | [platform.minimax.io](https://platform.minimax.io/) |
| **xAI** | Grok 3 | [console.x.ai](https://console.x.ai/) |
| **Ollama** | Any local model | [ollama.com](https://ollama.com/) |
| **OpenRouter** | Any model | [openrouter.ai](https://openrouter.ai/) |

Right-click the AI panel → **Quick Add Model** → paste your key. Done.

API keys are stored in an encrypted local keystore — never in config files or plain text.

## Getting started

### Download and run

Grab the latest release from the [downloads section](#download) above. Open the DMG, drag to Applications, launch.

### Build from source

```bash
git clone https://github.com/mits-pl/wove.git
cd wove
task init
task dev
```

Requires: macOS 11+ / Windows 10+ / Linux (glibc-2.28+), Node.js 22 LTS, Go 1.25+, [Task](https://taskfile.dev/)

### Set up your model

1. Open Wove → find the **AI panel** on the right (or `Alt+Shift+A`)
2. Right-click → **Quick Add Model** → pick your provider
3. Paste your API key → done

### Configure your project

Create `WAVE.md` in your project root:

```markdown
## Project
My App — Laravel 11, Inertia.js, Vue 3

## Conventions
- Always use Form Request classes for validation
- Use Eloquent scopes, not raw queries
- Run vendor/bin/pint after changes
```

Wove reads this on every request and enforces your rules at tool level.

### Advanced: custom model endpoints

For models not in Quick Add, create `~/.config/woveterm/waveai.json`:

```json
{
  "my-model": {
    "display:name": "My Model",
    "ai:apitype": "anthropic-messages",
    "ai:model": "claude-sonnet-4-6",
    "ai:endpoint": "https://api.anthropic.com/v1/messages",
    "ai:apitokensecretname": "my_api_key",
    "ai:capabilities": ["tools", "images", "pdfs"]
  }
}
```

Supported API types: `anthropic-messages`, `openai-responses`, `openai-chat`, `google-gemini`

## How it works

```
Your message
    ↓
[Project rules from WAVE.md/CLAUDE.md]
[MCP context: database schema, docs]
[Active plan: current step]
    ↓
Agent: plan → read → write → verify → test
    ↓
    ├── needs web data? → opens browser, reads page
    ├── complex task? → delegates to sub-task (isolated tab)
    └── done? → closes terminals and browsers it opened
```

## Guardrails

Not just prompt suggestions — enforced at tool level:

- **Read-before-edit** — tool rejects edits on unread files
- **Large file protection** — blocks full rewrites of files >200 lines, forces targeted edits
- **Architecture matching** — finds similar files, copies their patterns
- **Doom loop detection** — detects repeating tool calls, forces strategy change
- **Structured compaction** — when context grows, preserves action history instead of losing it
- **Security awareness** — parameterized queries, output escaping, no secrets in code

## Built on

[Wave Terminal](https://github.com/wavetermdev/waveterm) by [Command Line Inc.](https://www.commandline.dev/), Apache License 2.0.

See [MODIFICATIONS.md](MODIFICATIONS.md) for changes from upstream.

## Contributing

Issues and PRs welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Support

[![Buy Me A Coffee](https://img.shields.io/badge/Buy%20Me%20A%20Coffee-support-yellow?logo=buymeacoffee)](https://buymeacoffee.com/wove)

## License

Apache License 2.0. See [LICENSE](LICENSE).
