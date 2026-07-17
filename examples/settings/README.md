# Settings examples

solcode reads JSON config (usually `settings.json` / `settings.local.json`).

## Files

| File | Use when |
|------|----------|
| [`settings.minimal.example.json`](settings.minimal.example.json) | First install: one provider, sonnet + haiku |
| [`settings.multimodel.example.json`](settings.multimodel.example.json) | Multiple models / second provider (proxy) |
| [`settings.full.example.json`](settings.full.example.json) | Models + skills + MCP stub + hooks + memory |

Also: repo-root [`../config.multimodel.json`](../config.multimodel.json) is a short multimodel sample (legacy path).

## Install

```bash
# user config (recommended for API keys via env)
mkdir -p ~/.solcode
cp examples/settings/settings.minimal.example.json ~/.solcode/settings.json

# secrets / TUI custom models go here (gitignored by convention)
# ~/.solcode/settings.local.json  →  { "providers": [{ "name": "…", "api_key": "…" }] }

# project overrides
mkdir -p .solcode
cp examples/settings/settings.full.example.json .solcode/settings.json
```

Prefer **`api_key_env`** over embedding keys in JSON. If you must store a key, put it only in `settings.local.json` and keep that file out of git.

## Model fields

Per-model overrides win over top-level defaults when set:

| Field | Meaning |
|-------|---------|
| `name` | Short switch name (`/model`, CLI `-model`) |
| `id` | API model id sent to the provider |
| `display_name` | TUI label |
| `default` | Default selection when `model` omitted |
| `fast` | Candidate for `fast_model` / cheap sub-agents |
| `max_context_tokens` | Context window estimate |
| `max_tokens` | Output cap |
| `thinking` / `thinking_text` / `effort` | Extended thinking |

Switch at runtime: `/model`, `/provider`, `/effort`.

## Skills / hooks / MCP

- **skills.paths** — directories scanned for `SKILL.md` folders or `*.md` files (see [`../skills/`](../skills/))
- **hooks.events** — `UserPromptSubmit` / `PreToolUse` / `PostToolUse` / … (see [`../hooks/`](../hooks/))
- **mcp_servers** — stdio or HTTP MCP tools (`/mcp` to toggle)

Hook `command` strings run via the host shell (`bash -c` / `cmd /c`) with **cwd = agent workdir**, so relative paths like `node examples/hooks/...` work when you start solcode from the repo root. For installed binaries, use absolute paths or copy scripts into `~/.solcode/hooks/`.
