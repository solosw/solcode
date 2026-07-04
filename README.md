# codeplus-agent

A terminal-based coding agent powered by Claude (Anthropic API) that can read, write, edit, search, and reason about your codebase — all from the command line.

## Features

- **Interactive TUI** — Rich terminal UI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), with streaming text, thinking indicators, and permission dialogs.
- **Batch mode** — Run one-shot prompts non-interactively via `-prompt`.
- **Multi-model support** — Configure multiple LLM providers and models, switch at runtime with `/model`.
- **20+ built-in tools** — Bash, Edit, Write, View, Grep, Glob, LS, Diff, Patch, Fetch, WebSearch, LSP, MCP, TodoWrite, AskUser, Task (sub-agents), and more.
- **MCP (Model Context Protocol)** — Connect to external MCP servers over stdio or HTTP.
- **Custom skills** — Define reusable skill files loaded from configurable directories.
- **Hook system** — Execute shell commands on agent events (tool calls, results, completion).
- **Permission modes** — `auto`, `accept_edits`, `bypass`, `yolo`, `plan` — control how tools are authorized.
- **Sub-agent coordinator** — The `task` tool spawns isolated sub-agents for parallel or independent work.
- **LSP integration** — Go-to-definition, references, hover, and workspace symbols from your language servers.

## Quick Start

### Installation

```bash
go install github.com/solosw/codeplus-agent/cmd/codeplus@latest
```

### Prerequisites

- Go 1.25+
- An Anthropic API key (set `ANTHROPIC_API_KEY` environment variable)

### First run

```bash
export ANTHROPIC_API_KEY="sk-ant-..."

# Interactive mode
codeplus

# Batch mode
codeplus -prompt "Explain the architecture of this project"
```

## Usage

```
codeplus [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | (auto-discover) | Path to JSON config file |
| `-prompt` | (none) | Prompt to run; when omitted, launches TUI |
| `-workdir` | `$PWD` | Working directory for tool execution |
| `-max-turns` | from config | Maximum model/tool loop turns |
| `-timeout` | `30m` | Maximum run duration |
| `-model` | from config | Override model (name or ID) |

Config auto-discovery looks for `~/.agentcode/settings.json`, `~/.agentcode/settings.local.json`, `./.agentcode/settings.json`, and `./.agentcode/settings.local.json` in order; later files merge on top.

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
    "paths": [".agentcode/skills"],
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
|-------|------|-------------|
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

## TUI Commands

In interactive mode, type `/` to access commands:

| Command | Description |
|---------|-------------|
| `/model` | List available models |
| `/model <name>` | Switch to a different model |
| `/help` | Show available commands |

## Project Structure

```
codeplus-agent/
├── cmd/codeplus/main.go       # Entry point
├── internal/
│   ├── agent/                 # Coordinator & sub-agent orchestration
│   ├── anthropic/             # Anthropic API client
│   ├── app/                   # Application lifecycle & wiring
│   ├── config/                # Configuration loading & normalization
│   ├── db/                    # Database migrations (future)
│   ├── engine/                # Core prompt→model→tool loop
│   ├── hook/                  # Event-driven hook runtime
│   ├── lsp/                   # Language Server Protocol client
│   ├── mcp/                   # Model Context Protocol clients
│   ├── memory/                # Session memory (future)
│   ├── message/               # Message types
│   ├── permission/            # Tool authorization service
│   ├── pubsub/                # Pub/sub messaging
│   ├── session/               # Session management
│   ├── skill/                 # Custom skill loader
│   ├── tool/                  # All built-in tool implementations
│   └── tui/                   # Terminal UI (Bubble Tea)
├── embed/                     # Embedded files (prompts, migrations)
├── examples/                  # Example configs
├── api_tests/                 # API-level tests
└── unit_tests/                # Unit tests
```

## License

MIT
