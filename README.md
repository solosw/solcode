# solcode

A terminal-based coding agent powered by Claude (Anthropic API) that can read, write, edit, search, and reason about your codebase — all from the command line.

## Features

- **Interactive TUI** — Rich terminal UI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), with streaming text, inline diff rendering, syntax highlighting, thinking indicators, and permission dialogs.
- **Batch mode** — Run one-shot prompts non-interactively via `-prompt`.
- **Multi-model support** — Configure multiple LLM providers and models, switch at runtime with `/model`.
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

```bash
go install github.com/solosw/solcode/cmd/solcode@latest
```

### Prerequisites

- Go 1.25+
- An Anthropic API key (set `ANTHROPIC_API_KEY` environment variable)

### First run

```bash
export ANTHROPIC_API_KEY="sk-ant-..."

# Interactive mode
solcode

# Batch mode
solcode -prompt "Explain the architecture of this project"
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
| `-timeout` | `30m` | Maximum run duration |
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

### Slash Commands

Type `/` in the input to access commands:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/clear` | Clear the current TUI transcript |
| `/model` | Select a model via dialog |
| `/provider` | Select a provider via dialog |
| `/effort` | Select thinking effort (low/medium/high) |
| `/sessions` | List and load saved sessions |
| `/compact` | Compact the current session context |
| `/new-session [name]` | Create and switch to a new session |
| `/skills` | Browse skills and toggle enabled/disabled |
| `/mcp` | Browse MCP servers and toggle enabled/disabled |
| `/[skill] [args]` | Invoke a loaded skill by name |

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
  "providers": [
    {
      "name": "anthropic",
      "type": "anthropic",
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
      "tool_invoked": [
        {
          "matcher": "bash",
          "command": "echo 'bash was called'"
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
| `hooks.events` | map | Event → matcher+command mappings |

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
| `yolo` | Full auto-pilot with no confirmations |
| `plan` | Plan-only mode, no tool execution |

Switch modes at runtime with `Shift+Tab`.

## Project Structure

```
solcode/
├── cmd/solcode/main.go       # Entry point
├── internal/
│   ├── agent/                 # Coordinator & sub-agent orchestration
│   ├── anthropic/             # Anthropic API client & message types
│   ├── app/                   # Application lifecycle & wiring
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
