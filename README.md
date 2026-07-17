# solcode

A terminal-based coding agent powered by Claude (Anthropic API) that can read, write, edit, search, and reason about your codebase — all from the command line.

## Features

- **Interactive TUI** — Rich terminal UI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), with streaming text, inline diff rendering, syntax highlighting, timestamps, thinking indicators, and permission dialogs.
- **@ file attachments** — Type `@` to autocomplete and attach files from the working directory. Text files are inlined into the prompt; images are converted to multimodal image blocks for the model.
- **Persistent sessions** — Reload saved conversation history with its original message timestamps.
- **Batch mode** — Run one-shot prompts non-interactively via `-prompt`.
- **Multi-model support** — Configure multiple LLM providers and models, switch at runtime with `/model` and `/provider`, or add them directly from their dialogs.
- **20+ built-in tools** — Bash, Edit, Write, View, ViewImage, Grep, Glob, LS, Diff, Patch, Fetch, WebSearch, LSP, MCP, TodoWrite, AskUser, Task (sub-agents), and more.
- **MCP (Model Context Protocol)** — Connect to external MCP servers over stdio or HTTP.
- **Custom skills** — Define reusable skill files loaded from configurable directories.
- **Hook system** — Execute shell commands on agent events (tool calls, results, completion).
- **Permission modes** — `auto`, `accept_edits`, `bypass`, `yolo`, `plan` — control how tools are authorized.
- **Sub-agent coordinator** — The `task` tool spawns isolated sub-agents for parallel or independent work.
- **LSP integration** — Go-to-definition, references, hover, and workspace symbols from your language servers.
- **Inline diff rendering** — File edits (Edit/Write/Patch) show colored unified diffs directly in the TUI.
- **Syntax highlighting** — File content displayed in the TUI is syntax-highlighted via Chroma for 200+ languages.

## Quick Start

### Installation

**One-line install (no Go required)** — downloads the rolling **master** build
(published by CI on every push to `master`/`main`; there is no `latest` channel):

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/solosw/solcode/master/scripts/install.sh | bash
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/solosw/solcode/master/scripts/install.ps1 | iex
```

Options:

```bash
# custom install dir / fork (still tracks master by default)
curl -fsSL .../install.sh | bash -s -- --dir ~/bin
SOLCODE_REPO=myorg/solcode curl -fsSL .../install.sh | bash

# skip automatic PATH update
curl -fsSL .../install.sh | bash -s -- --no-path

# optional: pin a versioned release tag if you publish one
curl -fsSL .../install.sh | bash -s -- --version v0.1.0
```

```powershell
& .\scripts\install.ps1 -InstallDir "$env:USERPROFILE\bin"
# & .\scripts\install.ps1 -NoPath
# & .\scripts\install.ps1 -Version v0.1.0
```

Install scripts **add the binary directory to PATH automatically**:

- **Linux/macOS**: current session + shell rc (`.bashrc` / `.zshrc` / fish `config.fish`), idempotent managed block
- **Windows**: user `Path` env var + current PowerShell session (+ `WM_SETTINGCHANGE` broadcast)

**From source** (requires Go 1.25+):

```bash
go install github.com/solosw/solcode/cmd/solcode@master
# or
git clone https://github.com/solosw/solcode.git && cd solcode
go build -o solcode ./cmd/solcode
```

**How binaries are published**

| Trigger | Release tag | Asset names | Install default |
|---------|-------------|-------------|-----------------|
| Push to `master`/`main` | `master` (rolling, overwritten) | `solcode_master_<os>_<arch>.*` | yes |
| Push tag `v*` (optional) | `vX.Y.Z` | `solcode_vX.Y.Z_<os>_<arch>.*` | via `--version` |

Local build:

```bash
./scripts/build-release.sh master          # Linux/macOS
# .\scripts\build-release.ps1 -Version master   # Windows
```

### Prerequisites

- An Anthropic API key (set `ANTHROPIC_API_KEY` environment variable)
- For source builds only: Go 1.25+

### First run

```bash
export ANTHROPIC_API_KEY="sk-ant-..."

# Interactive mode
solcode

# Batch mode
solcode -prompt "Explain the architecture of this project"

# Check binary version (master builds show master+<sha>)
solcode -version
```

## Usage

```
solcode [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | (auto-discover) | Path to JSON config file |
| `-prompt` | (none) | Prompt to run; when omitted, launches TUI |
| `-workdir` | `$PWD` | Working directory for tool execution |
| `-max-turns` | from config | Maximum model/tool loop turns |
| `-timeout` | `0` (disabled) | Maximum run duration; `0` disables the per-conversation deadline |
| `-model` | from config | Override model (name or ID) |

Config auto-discovery looks for `~/.solcode/settings.json`, `~/.solcode/settings.local.json`, `./.solcode/settings.json`, and `./.solcode/settings.local.json` in order; later files merge on top.

## TUI Controls

### Keyboard Shortcuts

| Shortcut | Action |
|----------|--------|
| `Enter` | Send message |
| `Alt+Enter` | Insert newline |
| `Ctrl+C` | Cancel streaming / quit (when idle) |
| `Ctrl+T` | Toggle dark/light theme |
| `Ctrl+O` | Toggle collapse of last tool output |
| `Ctrl+A` | Select all text in input |
| `Ctrl+Shift+C` | Copy last assistant reply to clipboard |
| `Shift+Tab` | Cycle permission mode |
| `PageUp` / `PageDown` | Scroll chat view |
| `Ctrl+U` / `Ctrl+D` | Half-page scroll |
| `↑` / `↓` | Navigate input history |
| `Esc` | Exit select-all / close dialog |

### File attachments (`@`)

Type `@` in the input to attach files relative to the working directory:

- Autocomplete suggests files and directories (`↑`/`↓` + `Enter`/`Tab`)
- Text files are inlined into the user message for the model
- Images (png/jpg/gif/webp/…) are converted to Anthropic multimodal image blocks
- Paths with spaces: `@"my file.png"`

**Image context optimization**

Large images can dominate the context window. solcode applies the same pipeline for `@` image attachments and the `ViewImage` tool:

1. **Estimates vision tokens** using Anthropic’s formula after normalizing the longest edge to ≤1568px:  
   `tokens ≈ (width × height) / 750`
2. **Pre-resizes** images to a preferred max edge of **1280px** (never above 1568px)
3. **Re-encodes** as JPEG (quality 80) when smaller, so screenshots/photos use fewer bytes and tokens
4. **Counts image tokens** in live TUI `ctx` usage and in session token estimates (not only text)
5. **Sends real image blocks** — `@` attaches them on the user message; `ViewImage` returns them inside the `tool_result` (not a base64 text dump)

The model-facing attach note includes size and approximate token cost, e.g.  
`[attached image: shot.png, 4000x3000→1280x960, image/jpeg, ~1638 tokens, compressed 2.1MB→180KB]`.

Example:

```
Explain @internal/engine/engine.go and look at @screenshot.png
```

### Slash Commands

Type `/` in the input to access commands:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/clear` | Clear the current TUI transcript |
| `/model` | Select a configured model or add a custom model ID via dialog |
| `/provider` | Select a configured provider or add a custom provider via dialog |
| `/effort` | Select thinking effort (low/medium/high) |
| `/sessions` | List and load saved sessions |
| `/compact` | Compact the current session context |
| `/fix-session` | Repair incomplete tool-use exchanges in the current session |
| `/new-session [name]` | Create and switch to a new session |
| `/skills` | Browse skills and toggle enabled/disabled |
| `/mcp` | Browse MCP servers and toggle enabled/disabled |
| `/[skill] [args]` | Invoke a loaded skill by name |

### Add a provider or model from the TUI

Both the `/provider` and `/model` dialogs include a **Custom…** entry.

1. Run `/provider`, select **Custom…**, then enter the provider name, API key, and base URL in sequence.
2. The provider is written to the runtime settings file and the configuration is reloaded. It is intentionally created without a model.
3. Run `/model`, select **Custom…**, and enter the model ID. The model is saved with the same value for `name` and `id`, configuration is reloaded again, and future prompts use that model.

The runtime settings file is `~/.solcode/settings.local.json` by default. When solcode is started with `-config`, that explicit file is updated instead. Custom provider credentials are stored as the provider's `api_key` in this file; protect it as you would any API-key-containing configuration file. Press `Esc` while entering a custom value to return to the dialog choices, or `Ctrl+C` to cancel the dialog.

### Saved-session timestamps

Each saved message has a persisted timestamp. Reloading a session displays these original times rather than the time the TUI was reopened. Sessions created before per-message timestamps were available use their saved session update time as a stable fallback.

## Configuration

All configuration lives in a JSON file. Example:

```json
{
  "provider": "anthropic",
  "model": "opus",
  "max_turns": 20,
  "stream": true,
  "thinking": true,
  "thinking_text": false,
  "effort": "high",
  "permission_mode": "auto",
  "tui": {
    "theme": "dark",
    "background": "#101820"
  },
  "providers": [
    {
      "name": "anthropic",
      "api_key_env": "ANTHROPIC_API_KEY",
      "base_url_env": "ANTHROPIC_BASE_URL",
      "models": [
        {
          "name": "opus",
          "id": "claude-opus-4-8",
          "display_name": "Claude Opus 4.8",
          "default": true,
          "max_tokens": 64000,
          "thinking": true,
          "effort": "high"
        }
      ]
    }
  ],
  "skills": {
    "paths": [".solcode/skills"],
    "enabled": [],
    "disabled": []
  },
  "mcp_servers": [
    {
      "name": "my-server",
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@some/mcp-server"]
    }
  ],
  "hooks": {
    "events": {
      "PostToolUse": [
        {
          "matcher": "*",
          "hooks": [
            { "type": "builtin", "name": "compress_tool_result", "fail_mode": "open" }
          ]
        }
      ]
    }
  }
}
```

### Key configuration fields

| Field | Type | Description |
|------|------|-------------|
| `provider` | string | Active provider name |
| `model` | string | Active model name or ID |
| `max_turns` | int | Max model/tool loops per prompt |
| `stream` | bool | Enable streaming responses |
| `thinking` | bool | Enable extended thinking |
| `thinking_text` | bool | Show thinking text in TUI |
| `effort` | string | Thinking effort: `low`, `medium`, `high` |
| `permission_mode` | string | One of `auto`, `accept_edits`, `bypass`, `yolo`, `plan` |
| `providers` | array | Multi-provider configuration |
| `skills.paths` | array | Directories to scan for skill files |
| `mcp_servers` | array | MCP server definitions |
| `hooks.events` | map | Event → matchers; hook types: `command` or `builtin` |
| `tui.theme` | string | Initial palette: `dark` (default) or `light` |
| `tui.background` | string | TUI background color (hex or ANSI color index) |

**Eager tool-result compression (default)**

Large tool outputs are compressed on `PostToolUse` by the builtin `compress_tool_result` (headroom **legacy** path). Probe data on real sessions showed ~70–85% savings for tool dumps; the pipeline path was near 0%.

- Skips `Edit` / `Write` / `Patch` / `Diff`, errors, images, and small outputs (&lt; ~800 tokens)
- Applies only when savings ≥15% and ≥100 tokens
- `fail_mode: "open"` so failures never block tools

Disable:

```json
{
  "hooks": {
    "events": {
      "PostToolUse": [
        { "matcher": "*", "hooks": [{ "type": "builtin", "name": "disable_compress_tool_result" }] }
      ]
    }
  }
}
```

`ViewImage` returns a multimodal image block (after resize/re-encode) inside the tool result; the standard terminal TUI only shows the text caption, not the pixels.

**Examples pack** — see [`examples/`](examples/) for end-to-end samples:

| Area | Path |
|------|------|
| Model / full `settings.json` | [`examples/settings/`](examples/settings/) |
| Skills (`SKILL.md` workflows) | [`examples/skills/`](examples/skills/) |
| Hooks (Node / Python / Bash / PowerShell / Go) | [`examples/hooks/`](examples/hooks/) |

Hook scripts (multi-language): PreToolUse bash guard & input wrap, PostToolUse log/trim, UserPromptSubmit prefix, plus builtin `compress_tool_result`.

## Built-in Tools

| Tool | Description |
|------|-------------|
| `bash` | Run shell commands (with safety restrictions) |
| `edit` | Precise string-replacement edits in files |
| `write` | Create or overwrite files |
| `view` | Read file contents with line numbers |
| `view_image` | Read and display image files |
| `grep` | Search file contents by regex |
| `glob` | Find files by glob pattern |
| `ls` | List directory tree |
| `diff` | Preview unified diffs before writing |
| `patch` | Apply unified diff patches |
| `fetch` | Fetch content from URLs |
| `web_search` | Search the web and return structured results |
| `lsp` | Language server operations (definitions, references, hover, symbols) |
| `mcp` | Invoke MCP server tools |
| `todo_write` | Manage structured task lists |
| `ask_user` | Ask user questions in interactive dialogs |
| `task` | Spawn sub-agents for independent work |
| `skill` | Load and execute custom skills |

## Permission Modes

| Mode | Behavior |
|------|----------|
| `auto` | Ask for destructive operations, auto-approve reads |
| `accept_edits` | Auto-approve edits, ask for bash |
| `bypass` | Skip all permission prompts |
| `yolo` | Full auto-pilot with no confirmations (alias of bypass) |
| `plan` | Plan-only: read-only tools + `TodoWrite` + `Task`; each user message gets plan instructions (prefer sub-agent exploration, no file edits) |

Switch modes at runtime with `Shift+Tab`.

In **plan** mode, solcode prepends a planning system-style brief to every user message (not shown as a separate TUI bubble beyond the model transcript). Mutating tools (`Edit` / `Write` / `Patch` / `Bash` / …) are blocked; use `Task` to explore via sub-agents and `TodoWrite` to track the plan.

## Project Structure

```
solcode/
├── cmd/solcode/main.go       # Entry point
├── internal/
│   ├── agent/                 # Coordinator & sub-agent orchestration
│   ├── anthropic/             # Anthropic API client & message types
│   ├── app/                   # Application lifecycle & wiring
│   ├── attach/                # @path attachment expand (text inline + image blocks)
│   ├── config/                # Configuration loading & normalization
│   ├── db/                    # Database migrations & SQL queries
│   ├── engine/                # Core prompt→model→tool loop
│   ├── hook/                  # Event-driven hook runtime
│   ├── logging/               # Structured logging
│   ├── lsp/                   # Language Server Protocol client
│   ├── mcp/                   # Model Context Protocol clients (stdio/HTTP)
│   ├── memory/                # Cross-session memory & summarization
│   ├── message/               # Message type definitions
│   ├── permission/            # Tool authorization service
│   ├── pubsub/                # Internal pub/sub messaging
│   ├── session/               # Session persistence & compaction
│   ├── skill/                 # Custom skill loader
│   ├── tokenest/              # Token estimation utilities
│   ├── tool/                  # All built-in tool implementations
│   ├── tui/                   # Terminal UI (Bubble Tea)
│   │   ├── chat/              # Chat rendering components
│   │   ├── components/        # Reusable UI components
│   │   ├── dialog/            # Dialog rendering
│   │   ├── styles/            # Style definitions
│   │   ├── diff_render.go     # Inline diff colorization
│   │   ├── highlight.go       # Syntax highlighting (Chroma)
│   │   ├── markdown.go        # Markdown rendering (Glamour)
│   │   └── ...                # Model, messages, theme, commands
│   └── util/                  # Shared utilities
├── embed/                     # Embedded files (prompts, migrations)
├── examples/                  # Example configurations
├── api_tests/                 # API-level integration tests
└── unit_tests/                # Unit tests
```

## License

MIT
