# Settings examples

solcode reads JSON config (usually `settings.json` / `settings.local.json`).

## Files

| File | Use when |
|------|----------|
| [`settings.minimal.example.json`](settings.minimal.example.json) | First install: one provider, sonnet + haiku |
| [`settings.responses.example.json`](settings.responses.example.json) | OpenAI Responses (`/v1/responses`); replace `YOUR_RESPONSES_MODEL` with your provider's model ID |
| [`settings.multimodel.example.json`](settings.multimodel.example.json) | Multiple models / second provider (proxy) |
| [`settings.models.api.example.json`](settings.models.api.example.json) | User-selected subagent model plus independent embedding and memory-summary API settings; merge into chat settings |
| [`settings.full.example.json`](settings.full.example.json) | Models + skills + MCP stub + hooks + memory + hosted Jev (`type=api`) |
| [`settings.jev.local.example.json`](settings.jev.local.example.json) | Local Kev GGUF (GPU only, filename under ~/.solcode/models) — merge into your settings |
| [`settings.embedding.example.json`](settings.embedding.example.json) | Vector embeddings API (`type=api` OpenAI-compatible) |
| [`settings.embedding.local.example.json`](settings.embedding.local.example.json) | Local ONNX (`type=local` EmbeddingGemma; index under `ProjectStateDir/embeddings`, models fall back to `~/.solcode/embeddings`) |
| [`settings.ort.gpu.example.json`](settings.ort.gpu.example.json) | Legacy filename retained: now a GPU-only GGUF Jev + embedding example (no ORT) |
| [`settings.memory.organizer.example.json`](settings.memory.organizer.example.json) | Local memory organizer: in-process `llama.cpp` (GGUF) via `yzma`. Needs a GGUF model you supply plus the llama.cpp shared library; download the library from the Web UI or with `yzma install` |

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

## Skills / hooks / MCP / LSP

- **skills.paths** — directories scanned for `SKILL.md` folders or `*.md` files (see [`../skills/`](../skills/))
- **hooks.events** — `UserPromptSubmit` / `PreToolUse` / `PostToolUse` / … (see [`../hooks/`](../hooks/))
- **mcp_servers** — stdio or HTTP MCP tools (`/mcp` to toggle)
- **lsp** — language servers for the `LSP` tool (`enabled`, `include_defaults`, `servers[]` with `language` / `extensions` / `command`). Defaults auto-detect `gopls`, `pyright-langserver`, etc. when on `PATH`. See root [README LSP section](../../README.md#lsp-language-server-protocol) and [`settings.full.example.json`](settings.full.example.json).
- **jev** — TypeSafe System One decision layer (`type=api` hosted, or `type=local` Kev GGUF; local runs in-process on GPU, filename under ~/.solcode/models). See root [README Jev section](../../README.md#jev-decision-layer-optional) and [`settings.jev.local.example.json`](settings.jev.local.example.json).
- **memory.embedding** — Optional vector embeddings (`type=api` OpenAI-compatible, or `type=gguf` local; legacy `embedding` and `type=local` are accepted). API mode has its own endpoint, key and model. Project index lives beside `knowledge.db`; local models use `~/.solcode/models`. See [`settings.embedding.example.json`](settings.embedding.example.json).
- **memory.organizer** — Summarizes sessions and extracts durable memory after turns/compaction. Off by default. `runtime=yzma` (default) uses a local GGUF and llama.cpp; `runtime=api` uses explicit `base_url` / `api_key` (or their `_env` fields), `model` and `api_format` (`openai`, `anthropic`, `responses`). Local mode never falls back to the network; API mode sends session content to the configured endpoint. See [Memory organizer](../../README.md#memory-organizer-optional) and [`settings.models.api.example.json`](settings.models.api.example.json).
- **subagents.model** — User-selected default child model ID on the active chat provider, or `fast`. Empty inherits the main model. Role TOML `model` overrides this default. AI `model`, `difficulty`, and `fast_model` tool arguments no longer select models.

Hook `command` strings run via the host shell (`bash -c` / `cmd /c`) with **cwd = agent workdir**, so relative paths like `node examples/hooks/...` work when you start solcode from the repo root. For installed binaries, use absolute paths or copy scripts into `~/.solcode/hooks/`.
