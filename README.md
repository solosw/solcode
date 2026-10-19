# solcode

A terminal-based coding agent powered by Claude (Anthropic API) that can read, write, edit, search, and reason about your codebase — all from the command line.

## Features

- **Interactive TUI** — Rich terminal UI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), with streaming text, inline diff rendering, syntax highlighting, timestamps, thinking indicators, and permission dialogs.
- **@ file attachments** — Type `@` to autocomplete and attach files from the working directory. Text files are inlined into the prompt; images are converted to multimodal image blocks for the model.
- **Persistent sessions** — Reload saved conversation history with its original message timestamps. Project-scoped runtime state is stored outside the source tree under `~/.solcode/projects/<safe-project-path>/` (sessions, memories, todos, and knowledge graph).
- **Batch mode** — Run one-shot prompts non-interactively via `-prompt`.
- **ACP (Agent Client Protocol)** — Speak JSON-RPC over stdio with `solcode --acp` (or `solcode acp`) so editors like Zed can drive the same agent loop as the TUI. Supports streaming updates, permissions, cancel, session modes/load, tool-call diffs, ACP `plan` updates from `TodoWrite`, and capability-gated client `fs/read_text_file` / `fs/write_text_file`.
- **Multi-model support** — Configure multiple LLM providers and models, switch at runtime with `/model` (current provider only) and `/provider`, or add them directly from their dialogs.
- **Native Anthropic transport** — The Anthropic Messages API uses a handwritten HTTP/JSON/SSE client, including streaming text, thinking, and tool-input deltas; the official SDK remains only for internal message compatibility.
- **20+ built-in tools** — Bash / Task (long-running calls automatically yield to the background with completion notifications), ImageGenerate / ImageEdit (optional OpenAI-format Images API, separately configurable), ComputerUse (optional embedded open-computer-use native runtime; off by default), Edit, Write, View, ViewImage, Grep, Glob, LS, Diff, Patch, Fetch, WebSearch, LSP, MCP, TodoWrite, WriteMemory, ReadMemory, WriteSessionMemory, ReadSessionMemory, AskUser, Task (orchestrates internal Subagent workers), and more.
- **Checkpoints & rewind** — Before any file-mutating tool runs, solcode snapshots the file's turn-start contents. During a Bash call it instead compares before/after SHA-256 workdir fingerprints (capped at 2000 files / 1 MiB each, skipping `.git`, `node_modules`, binaries, and hidden paths) and captures whatever changed. `/rewind` then restores workspace files to the start of a past turn — **code only**; conversation history is untouched. Checkpoints may be labeled with `/checkpoint-name` and listed with `/checkpoints`.
- **Two memory layers** — `WriteMemory` / `ReadMemory` persist durable facts that stay true across sessions (preferences, project rules, verified commands). `WriteSessionMemory` / `ReadSessionMemory` keep this project's chronological session log in `<project>/.solcode/solcode.md`, where each entry records the checkpoint turn, the files changed, the timestamp, and the session id.
- **MCP (Model Context Protocol)** — Connect to external MCP servers over stdio or HTTP.
- **Custom skills** — Define reusable skill files loaded from configurable directories. When `computer_use.enabled` is true, solcode also loads a bundled `computer-use` skill that teaches the screenshot → act → verify loop.
- **Computer Use (optional)** — Opt-in desktop GUI automation (`ComputerUse` tool + bundled skill). Explicitly select the `open-computer-use` backend; releases embed its checksum-pinned native runtime without CGO. The tool is **not** core: discover it via `/computer-use` / Skill or ToolSearch.
- **Project rules** — Markdown instructions in `<project>/.solcode/rules.md` and `.solcode/rules/*.md` are injected into the system prompt at startup.
- **Hook system** — Execute shell commands on agent events (tool calls, results, completion).
- **Permission modes** — `auto`, `accept_edits`, `bypass`, `plan`, `goal`, `ultra` — control how tools are authorized.
- **Sub-agent coordinator** — The `task` tool spawns isolated sub-agents for parallel or independent work.
- **LSP integration** — Multi-language code intelligence via Language Server Protocol: go-to-definition, find references, hover, document/workspace symbols, and go-to-implementation. When enabled, `LSP` is a core model-visible tool on every turn. Language servers are launched by file extension (gopls, pyright, typescript-language-server, …); binaries on `PATH` are auto-detected, and you can override commands in settings.
- **Inline diff rendering** — File edits (Edit/Write/Patch) show colored unified diffs directly in the TUI.
- **Syntax highlighting** — File content displayed in the TUI is syntax-highlighted via Chroma for 200+ languages.

## Quick Start

### Installation

**One-line install (no Go required)** — downloads the rolling **master**
`*_computeruse` build (embedded native runtime; published by CI on every push to
`master`/`main`; there is no `latest` install channel):

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/solosw/solcode/master/scripts/install.sh | bash
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/solosw/solcode/master/scripts/install.ps1 | iex
```

Desktop automation still needs `"computer_use": {"enabled": true}` in settings —
see [Computer Use](#computer-use-optional). Then use the `/computer-use` skill.
`--computeruse` / `-ComputerUse` remain accepted for compatibility but are no
longer required (every published asset is that flavor).

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

**From source** (requires Go 1.26.2+; see `go.mod`):

```bash
go install github.com/solosw/solcode/cmd/solcode@master
# or
git clone https://github.com/solosw/solcode.git && cd solcode
# rolling release flavor (embedded native runtime):
./scripts/build-computeruse.sh master "$(go env GOOS)" "$(go env GOARCH)" tar.gz
```

**How binaries are published**

| Trigger | Release tag | Asset names | Install default |
|---------|-------------|-------------|-----------------|
| Push to `master`/`main` | `master` (rolling, overwritten) | `solcode_master_<os>_<arch>_computeruse.*` | yes |
| Push tag `v*` (optional) | `vX.Y.Z` | `solcode_vX.Y.Z_<os>_<arch>_computeruse.*` | via `--version` |

Local computer-use build (matches CI):

```bash
./scripts/build-computeruse.sh master linux amd64 tar.gz
# Windows: ./scripts/build-computeruse.sh master windows amd64 zip
```

### Prerequisites

- An Anthropic API key (set `ANTHROPIC_API_KEY` environment variable)
- For source builds only: Go 1.26.2+ (toolchain pin in `go.mod`)
- Optional: language servers on `PATH` for the [LSP](#lsp-language-server-protocol) tool (e.g. `gopls`, `pyright-langserver`)
- Optional local Jev: OpenJev/Laya model directory; local defaults to `engine=ort` (CPU ONNX Runtime auto-installed under `~/.solcode/lib` on Windows/Linux when missing). ORT loads in the background so startup is not blocked; early decisions may use deterministic fallbacks until the session is ready
- Optional local ORT GPU: default is CPU; set `ort.gpu=true` and install a CUDA-capable ORT build (see [Local ORT GPU](#local-ort-gpu-optional))
- Optional local memory organizer: a GGUF model you supply plus the `llama.cpp` shared library, loaded in-process via [yzma](https://github.com/hybridgroup/yzma) (no CGO). Get the library from the Web UI or `yzma install` (see [Memory organizer](#memory-organizer-optional))

### First run

```bash
export ANTHROPIC_API_KEY="sk-ant-..."

# Interactive mode
solcode

# Batch mode
solcode -prompt "Explain the architecture of this project"

# Agent Client Protocol (stdio JSON-RPC)
solcode --acp
# or: solcode acp

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
| `-acp` | off | Run as an Agent Client Protocol server on stdio (mutually exclusive with `-prompt` / TUI) |

Config auto-discovery looks for `~/.solcode/settings.json`, `~/.solcode/settings.local.json`, `./.solcode/settings.json`, and `./.solcode/settings.local.json` in order; later files merge on top.

### Image generation / edit (optional)

`ImageGenerate` and `ImageEdit` use an OpenAI-compatible Images API (`POST /v1/images/generations`, `POST /v1/images/edits`). Tools register only when `image.base_url` and `image.api_key` are set, independent of the chat provider:

```json
{
  "image": {
    "base_url": "https://api.openai.com",
    "api_key_env": "OPENAI_API_KEY",
    "model": "dall-e-3",
    "edit_model": "dall-e-2",
    "size": "1024x1024",
    "timeout_sec": 120,
    "output_dir": ".solcode/images"
  }
}
```

Env fallbacks: `OPENAI_API_KEY`, `OPENAI_IMAGE_BASE_URL` / `OPENAI_BASE_URL`. Use `"enabled": false` to hide tools. Default save dir: `<workdir>/.solcode/images/`.

### Computer Use (optional)

Desktop accessibility, screenshots, and app-scoped input use the native binary
from [open-codex-computer-use](https://github.com/iFurySt/open-codex-computer-use).
The legacy robotgo tool is **disabled**, even in old `-tags computeruse` builds.
Existing enabled-only settings do **not** opt into the new backend. Enable it
explicitly, or turn Computer Use on in the settings UI:

```json
{
  "computer_use": {
    "enabled": true,
    "backend": "open-computer-use"
  }
}
```

Release builds embed the **v1.1.1** upstream runtime, pinned by SHA256 in
`internal/computeruse/bundle/release.json`. The historical `_computeruse` asset
suffix is retained for installer compatibility, but the SolCode host is now
built with **CGO disabled**, without robotgo or native GUI build toolchains.
Legacy ONNX Runtime inference is unavailable in pure-Go builds (CGO builds retain
it); the current GGUF local-model backend is unchanged.
The first call extracts the verified runtime into `~/.solcode/computer-use/`;
there are no runtime downloads, npm installs, or Node dependencies. On macOS,
the complete signed `.app` bundle (including helpers and resources) is retained.
Upstream MIT and third-party notices are embedded and installed beside it.

Build a release archive for one platform:

```bash
./scripts/build-computeruse.sh dev darwin arm64 tar.gz
```

Or prepare a source build for your target (use the same GOOS/GOARCH when building):

```bash
python3 scripts/prepare-computer-use.py --os darwin --arch arm64
CGO_ENABLED=0 go build -o solcode ./cmd/solcode
```

Alternatively, set `computer_use.binary` to an installed native executable. A
source build without an embedded runtime also checks `open-computer-use` on PATH.
Missing binaries or desktop permissions cause tool errors, **not ACP startup
failures**. The runtime launches lazily on first ComputerUse call, persists across
calls to retain native element indices, and closes on application exit/reload.

macOS requires **14.0+**, Accessibility and Screen Recording permissions for Open
Computer Use. Windows/Linux require a logged-in desktop. Linux still needs
Python 3, PyGObject/GTK and AT-SPI runtime support; pure-Go host builds do not remove
these upstream desktop dependencies. Wayland support depends on the desktop and
available input backends. This integration does not automatically install or
activate upstream Locked Use privileged helpers or global-pointer fallbacks.

The bundled **computer-use** skill (`/computer-use` or Skill tool) explains the
new API. ComputerUse is not a core tool: discover it via Skill/ToolSearch. Example:

```json
{"action":"list_apps"}
{"action":"get_app_state","arguments":{"app":"TextEdit"}}
{"action":"describe","arguments":{"tool":"click"}}
{"action":"click","arguments":{"app":"TextEdit","element_index":"0"}}
```

Use `describe` for native argument schemas instead of guessing. Screenshots are
returned as vision blocks, not base64 text. Mutating actions retain SolCode's
normal permissions and Jev guardrail checks. Old screenshot-first robotgo
parameters are intentionally rejected; refresh app state before acting.

### Jev decision layer (optional)

[Jev](https://docs.typesafe.ai) is a TypeSafe **System One** model. It does not
generate text, write code, or call tools — it evaluates a state and returns
typed answers with calibrated probabilities: a `Choice` from a fixed option set,
a `Score` along ordered levels, or a `Noul` (the probability a yes/no statement
is true). solcode uses it as a **decision layer** alongside the chat model, never
as a replacement for it.

Install the `typesafe-sdk` docs skill or read the [primitives](https://docs.typesafe.ai/primitives)
before writing new questions.

```json
{
  "jev": {
    "enabled": true,
    "api_key": "ts_...",
    "base_url": "https://api.typesafe.ai",
    "model": "jev-latest",
    "timeout_sec": 20,
    "route_min_confidence": 0.6,
    "routing": true,
    "memory_judge": true,
    "guardrail": true
  }
}
```

Env fallbacks: `TYPESAFE_API_KEY`, `TYPESAFE_BASE_URL` (or the `*_env` fields).
Everything is off by default, and `enabled` without a resolvable `api_key` is
treated as off.

Both this and Computer Use are also configurable from the Web UI
(`/workflow-ui` → **Settings** → *Features* / *Jev (System One)*). Saving writes
to `~/.solcode/settings.local.json` and reloads the running app, so no restart
is needed. The UI never receives a stored API key back — it only reports whether
one is set, and leaving the field blank keeps the existing key.

**What it does when enabled**

| Toggle | Effect |
|---|---|
| `routing` | When lexical tool/skill matching finds nothing, asks Jev which capability the request describes and enables the winners. Also registers the bundled workflow skills and picks one per prompt. |
| `memory_judge` | Classifies memories (kind / scope / tier / durability) with typed questions instead of asking a chat model to emit and then parse JSON. |
| `guardrail` | Advisory pre-action check on mutating tools (`Bash`, `Write`, `Edit`, `MultiEdit`, `MultiWrite`, `Patch`, `ComputerUse`) for secrets, destructive actions, exfiltration, and privilege escalation. |

**Bundled workflow skills (Jev mode only)**

With `routing` on, solcode registers a bundled skill set and asks Jev to pick
one per prompt:

| Skill | Use |
|---|---|
| `explore` | Read-only reconnaissance: locate entry points, trace wiring, map structure before editing. |
| `implement` | Make a code change end to end in the existing style, with a minimal diff. |
| `verify` | Run the real build/tests, check the failure baseline, report honestly. |
| `research` | Answer from official docs and API references instead of guessing. |
| `debug` | Reproduce, hypothesize, test, fix the cause, add a regression test. |
| `review` | Critique a diff: correctness, regressions, missing cases, scope creep. |

They are materialized to `~/.solcode/builtin-skills/<name>/` so each skill's
`Root` is a real path. When Jev selects a skill it is **force-loaded** into the
conversation — the same text the `Skill` tool would return — so the selection is
applied rather than merely advertised and the model does not need a `Skill` call
to pick it up.

Choosing **none** is an explicit hand-back: no skill is loaded and the full
catalog is advertised, leaving the decision to the model. That is the path taken
whenever Jev is off, declines, or returns an unknown name.

These skills are **not** registered without Jev routing, and `skills.disabled` /
`skills.enabled` still apply.

**Design rules this integration follows**

- **Jev only advises.** Skill/tool selection, memory classification, and the
  safety check all keep a deterministic fallback. Turn Jev off and behavior is
  identical to before it existed.
- **Failures fail closed, never open.** An unreachable guardrail *withholds* a
  call rather than allowing it. A routing miss falls back to the lexical result.
- **No automatic approval.** Confidence is never used to grant permission for a
  destructive action. The permission service and sandbox remain the controls;
  the guardrail can only add friction.
- **Whole-batch requests.** Questions sharing a state go in one request, since
  System One evaluates them in parallel and extra questions cost only tokens.
- **One Noul per candidate, not one Choice.** Tool screening asks a separate
  yes/no question about each candidate, because a request can need several
  tools at once and a Choice would return a single winner.

### Memory organizer (optional)

> **Status: wired into compaction.** When enabled, compact triggers an async
> local organizer run (session recall → `.solcode/solcode.md`, archival
> candidates → durable memories), hides `WriteMemory` / `WriteSessionMemory`,
> keeps `Read*` tools, and runs lifecycle consolidation. Opt-in new sessions
> can bootstrap core + archival memory into the first prompt (Letta-style).
> Quality still depends on the local GGUF model.

A **fully local** GGUF model that summarizes finished sessions and extracts
durable memory. It runs **in-process** through `llama.cpp`, loaded with
[`hybridgroup/yzma`](https://github.com/hybridgroup/yzma) over purego FFI — no
CGO, no external server, and no remote endpoint. There is deliberately no API
mode and no fallback to the chat provider: if the local model cannot load, the
organizer is simply unavailable.

Off by default.

**What you need to provide**

| Piece | How to get it | Notes |
|---|---|---|
| llama.cpp shared library | **Web UI download** (see below), or `yzma install` | Platform-specific binary; solcode binds it at runtime |
| GGUF model | **Your own file** | Not downloaded by solcode; pick the size you can afford |

```json
{
  "memory": {
    "organizer": {
      "enabled": true,
      "model": "Qwen3.5-0.8B-BF16.gguf",
      "context_size": 8192,
      "max_output_tokens": 1500,
      "temperature": 0.2,
      "timeout_sec": 180
    }
  }
}
```

| Field | Meaning |
|---|---|
| `enabled` | Turns the organizer on. |
| `model` | GGUF filename in ~/.solcode/models; required when enabled. |
| `context_size` | Context window in tokens (default 16384). Bounds how much transcript fits. |
| `max_output_tokens` | Completion cap for one organizer run. |
| `temperature` | Sampling temperature. `0` selects greedy decoding. |
| `timeout_sec` | Per-request deadline (default 180, max 900). |

See also [`examples/settings/settings.memory.organizer.example.json`](examples/settings/settings.memory.organizer.example.json).

**Getting the llama.cpp library**

Two ways — both need no compiler and no CGO.

*From the Web UI* — open `/workflow-ui` → **Settings** → *Memory Organizer (local
model)* and press **Download llama.cpp library**. The
settings card shows whether the model file and the library were found on disk,
and the download runs in the background with a progress bar. Leave the page if
you like; reopening it resumes showing the install state.

*From the CLI* — use the `yzma` tool, which fetches a build matching your yzma
version:

```bash
go install github.com/hybridgroup/yzma/cmd/yzma@latest
yzma install --processor metal        # macOS; use cuda on Windows/Linux
yzma install -l /path/to/libs         # equivalent to YZMA_LIB
```

**The library and the yzma version must match.** Letting `yzma install` (or the
Web UI button) choose the build is the reliable path; pointing `YZMA_LIB` at a
random `llama.cpp` build may fail to bind. The settings card reports the
installed build when the record is present.

**Restart after downloading.** The library is bound process-wide at load time,
so a completed download does not take effect until solcode restarts. The probe
line updates immediately, but the in-memory generator still uses the old
binding.

**Where things live** (all under your home directory by default):

```
~/.solcode/lib/llama/     # llama.cpp shared library (+ yzma-install.json record)
~/.solcode/models/        # all local GGUF model files (filenames in settings)
```

Env override: YZMA_LIB for the library directory. Model files always resolve under
~/.solcode/models. GPU is required; native failures never retry on CPU.

**Notes**

- Loading a multi-gigabyte GGUF takes seconds to tens of seconds. The loader is
  written to run on a background goroutine so startup is never blocked, and to
  report itself unavailable until ready.
- The UI automatically selects Metal on macOS and CUDA on Windows/Linux.
  No CPU/GPU selector or automatic CPU retry is available.
- The organizer validates its own output: candidates with an unknown kind or
  tier, or containing secrets, are discarded rather than kept.

### Local Jev GGUF (GPU only)

Local Jev uses Kev's trained decision head through the same in-process
llama.cpp shared library as embedding and organizer. No server or HTTP bridge
is started. It renders Kev's training layout, reads option-end and final
pointer vectors, computes scaled dot products, and applies the calibration
temperature stored in the GGUF. No text tokens are generated.

All local models use filenames under **~/.solcode/models**:

```json
{
  "jev": {"enabled": true, "type": "local", "model": "Kev-4B-Q8_0.gguf", "routing": true},
  "memory": {
    "embedding": {"enabled": true, "type": "gguf", "model": "embeddinggemma-2-BF16.gguf"},
    "organizer": {"enabled": true, "model": "Qwen3.5-0.8B-BF16.gguf"}
  }
}
```

GPU offload is mandatory for all three models. Missing GPU support, allocation
failure, or a missing library produces an unavailable feature / deterministic
Jev decision fallback, **not CPU inference**. Models load lazily and unload after
idle time; they share a native runtime lock and GPU ownership.

This backend currently supports **Kev pointer-head GGUFs**, not arbitrary chat,
OpenJev label-readout, or Laya GGUFs. The library must support Kev's classifier
projection (llama.cpp decision-model support). Context is capped at 4096 tokens;
overlong requests fail rather than silently dropping decision options.

**Library compatibility:** an older llama.cpp build may not recognize the newer
`gemma-embedding2` architecture (including embeddinggemma-2-BF16.gguf). Such
a model remains unavailable until a matching runtime is installed; there is no CPU
or API fallback.

Old ONNX controls (model_dir, dtype, engine=ort, ort_lib, ort.gpu) are obsolete
and removed on settings save. Existing organizer/embedding model_path values
migrate to filenames; move the corresponding weights to ~/.solcode/models if
necessary. ONNX weights are not converted automatically. Legacy ONNX adapter
code remains for existing low-level tests but is no longer wired into the app.

### Context and tool-result handling

Three behaviors keep the prompt small without hiding capability from the model:

- **Folded tool summary.** Only core, sticky, and matched tools are sent as
  schemas. The rest are summarized in one block — `MCP browser: click, type,
  navigate` — so the model knows what exists and searches for it, instead of
  assuming an unlisted capability is absent.
- **Tool screening over lexical candidates.** When lexical matching finds
  nothing, Jev screens the strongest candidates with one Noul each and enables
  every one that clears the confidence floor. The candidate set is bounded
  (24) and pre-ordered lexically, so the batch stays cheap.
- **MCP results as readable text.** JSON returned by an MCP server is rendered
  as indented key/value lines rather than a raw blob, so the model reasons about
  the content instead of parsing syntax. Rendering is lossless — every leaf
  value survives — and plain prose passes through unchanged.

### Project rules

Project-only agent instructions live under the working directory's `.solcode` folder and are injected into the system prompt at startup (not user-level `~/.solcode`):

```
<project>/.solcode/rules.md          # optional single file
<project>/.solcode/rules/*.md        # optional extra files, loaded in name order
```

```markdown
# .solcode/rules.md
Always run go test after code changes.
Prefer table-driven tests.
```

Missing files are ignored. These rules apply only to this project.

### Agent Client Protocol (ACP)

Run solcode as an ACP agent so a compatible client (for example Zed) owns the UI while solcode runs the same tool loop:

```bash
solcode --acp
# or: solcode acp
```

Notable session behavior:

| Area | Behavior |
|------|----------|
| Streaming | Thought, message, tool-call, and usage updates over `session/update` |
| Permissions | `session/request_permission` with allow/reject once or always; edit tools can include a diff preview |
| Cancel | `session/cancel` cancels the in-flight prompt (`stopReason=cancelled`) |
| Modes | `session/set_mode` maps to permission modes and emits `current_mode_update` |
| History | `session/load` replays saved user/assistant turns |
| Cross-session memory | `session/new` enables cross-session memory by default and schedules first-prompt recall when memory is enabled. `session/load` preserves the saved choice; `/new-session` still offers an explicit yes/no choice. |
| Tool diffs | `Edit` / `Write` / `MultiEdit` / `MultiWrite` emit ACP `type: "diff"` content plus `locations` |
| Plan updates | Successful `TodoWrite` calls emit a full `plan` (`sessionUpdate: "plan"`) update; when every item is `completed`, the agent sends `entries: []` (cleared plan) |
| Client filesystem | When the client advertises FS capabilities at `initialize`, text tools use per-session `fs/read_text_file` and `fs/write_text_file`; otherwise they fall back to the local disk. `TodoWrite` always stays on the agent-local todo file (never client FS). In `bypass` / `goal` modes, writes also fall back to local disk so the client does not show a second file-accept prompt |

Client FS access is capability-gated per session (captured at `session/new` / `session/load`). `ViewImage` stays on the local path because ACP text-file RPCs are text-only.

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
| `/model` | Select a model from the **current provider**, or add a custom model ID via dialog |
| `/provider` | Select a configured provider or add a custom provider via dialog |
| `/effort` | Select thinking effort (low/medium/high) |
| `/sessions` | List and load saved sessions |
| `/compact` | Compact the current session context |
| `/checkpoints` | List code checkpoints for the current session |
| `/checkpoint-name <name> [turn]` | Label a checkpoint (defaults to the newest turn) |
| `/rewind <turn\|name>` | Restore workspace files to a previous turn (code only) |
| `/fix-session` | Repair incomplete tool-use exchanges in the current session |
| `/new-session [name]` | Create and switch to a new session |
| `/skills` | Browse skills and toggle enabled/disabled |
| `/mcp` | Browse MCP servers and toggle enabled/disabled |
| `/proxy [url|on|off|clear]` | Show or set the HTTP(S) proxy used for API/Fetch/MCP requests |
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
| `permissions.bypass_subagents` | bool | Enable `Task` and explorer/worker/reviewer sub-agents in bypass; default `false`, does not affect ultra |
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

## LSP (Language Server Protocol)

solcode talks to **external language servers** over stdio (JSON-RPC). The agent uses one built-in `LSP` tool; which server runs depends on the file extension.

### Operations

| Operation | Purpose |
|-----------|---------|
| `go_to_definition` | Jump to symbol definition |
| `find_references` | List references (impact analysis / rename prep) |
| `hover` | Type / signature / docs at a position |
| `document_symbol` | Outline symbols in a file |
| `workspace_symbol` | Search symbols across the workspace |
| `go_to_implementation` | Find interface implementations |

Positions are **1-based** (`line` / `character`), matching editor conventions.

### Defaults (auto if binary is on PATH)

| Language | Extensions | Command |
|----------|------------|---------|
| Go | `.go` | `gopls` |
| Python | `.py`, `.pyi` | `pyright-langserver --stdio` |
| TypeScript / JS | `.ts`, `.tsx`, `.js`, `.jsx`, `.mjs`, `.cjs` | `typescript-language-server --stdio` |
| Rust | `.rs` | `rust-analyzer` |
| C / C++ | `.c`, `.h`, `.cpp`, `.cc`, `.cxx`, `.hpp`, `.hxx` | `clangd` |
| Java | `.java` | `jdtls` |

Install the server yourself (examples):

```bash
go install golang.org/x/tools/gopls@latest
npm install -g pyright typescript typescript-language-server
# rustup component add rust-analyzer   # or install rust-analyzer binary
```

If no server is registered for a file type (or the binary is missing), the tool returns `language server is not available` and the agent can fall back to Grep/View.

### Configuration

In `~/.solcode/settings.json` or project `.solcode/settings.json`:

```json
{
  "lsp": {
    "enabled": true,
    "include_defaults": true,
    "servers": [
      {
        "language": "go",
        "extensions": [".go"],
        "command": ["gopls"]
      },
      {
        "language": "python",
        "extensions": [".py", ".pyi"],
        "command": ["pyright-langserver", "--stdio"]
      }
    ]
  }
}
```

| Field | Default | Meaning |
|-------|---------|---------|
| `enabled` | `true` | Register the LSP tool |
| `include_defaults` | `true` | Merge built-in language mappings (only if the binary exists on `PATH`) |
| `servers` | `[]` | User-defined servers; same language or overlapping extensions **override** defaults |
| `servers[].disabled` | `false` | Skip this entry |

Sessions are reused per `(workDir, language)` for the life of the process and shut down on app exit / feature reload.

### Anthropic Messages transport

For `api_format: "anthropic"` (the default), solcode sends native Messages API requests through its own HTTP/JSON transport rather than the official SDK client. It supports non-streaming responses and SSE streams, including text, thinking, and incremental `tool_use` JSON input. Before a response body is consumed, transient transport failures and HTTP `408`, `409`, `429`, and `5xx` responses are retried up to five times with capped exponential backoff; `Retry-After` is honored when present. Existing engine and session code continues to use SDK-shaped message types as a compatibility boundary.

### OpenAI Responses transport

Set `api_format: "responses"` to use `/v1/responses` instead of Chat Completions. This can be configured globally or on an individual provider (`openai_responses` and `openai-responses` are also accepted). The existing `api_format: "openai"` continues to use Chat Completions.

```json
{
  "provider": "openai-responses",
  "model": "coding",
  "stream": true,
  "providers": [
    {
      "name": "openai-responses",
      "api_format": "responses",
      "api_key_env": "OPENAI_API_KEY",
      "base_url": "https://api.openai.com",
      "models": [{ "name": "coding", "id": "YOUR_RESPONSES_MODEL" }]
    }
  ]
}
```

`base_url` is required and may be an origin, a `/v1` API root, or the complete `/v1/responses` endpoint; the transport avoids adding `/v1` twice. Choose a model supported by your Responses provider.

The adapter supports non-streaming JSON and SSE, text and image inputs (including `view_image` results), function calls and results, reasoning summaries, and cached-token usage accounting. It maps `max_tokens` to `max_output_tokens`, `effort` to `reasoning.effort`, and enabled `thinking_text` to `reasoning.summary: "auto"`. Tool schemas are sent with `strict: false` to preserve optional fields. Each request sends the engine's full history with `store: false`; server-side conversation state is not used. Encrypted reasoning items are requested and replayed through the existing SDK-shaped history so reasoning-enabled tool turns can continue without server-side storage. Incomplete responses report the appropriate token-limit/refusal stop reason, while failed or interrupted streams return errors rather than silently accepting partial output. HTTP authentication and retry behavior are shared with the Chat Completions transport.

See also [`examples/settings/settings.full.example.json`](examples/settings/settings.full.example.json).

## Background Bash / Task execution

The main agent no longer has to block on a long shell command or task graph. By default, `Bash` and `Task` calls that are still running after **10 seconds of actual waiting** yield a `running_in_background` acknowledgement, and the agent can continue independent work. Both tools accept `run_in_background: true` to yield immediately. Up to eight calls can be detached per main-agent run; additional calls run in the foreground.

```json
{
  "background_tasks": {
    "enabled": true,
    "yield_after_ms": 10000
  }
}
```

- `yield_after_ms` is the foreground waiting interval, **not** the execution deadline. Bash's `timeout` still limits the command's total runtime (default 60 seconds, maximum 24 hours); specify a larger `timeout` for builds, training, or other lengthy commands. Task retains its executor deadline of 30 minutes.
- Completion, failure, and output are inserted as a background-task notification at the next safe model boundary. The original tool call receives exactly one `tool_result`; there is no duplicate result or forced cancellation of an in-flight model response.
- If the main model has nothing else to do, the runtime waits locally for completion rather than polling the model. Queued user prompts can wake this idle wait and allow more independent work.
- Background jobs are owned by the current main-agent run. Canceling the run, reaching its turn limit, or encountering a model error cancels unfinished jobs and records cancellation. They do not survive an application restart or transfer to another session.
- A background `Task` keeps its entire dependency graph and child agents running normally. Nested agents keep synchronous tool execution so their dependent work cannot continue with unfinished results. Permissions, validation, execution timeouts, and pre/post-tool hooks still apply.
- Do not start dependent work or assume success until the completion notification arrives. Avoid concurrent edits to the same files from the main agent and background tasks.

Set `background_tasks.enabled` to `false` to retain foreground-only execution.

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
| `LSP` | Core read-only language intelligence: definition, references, hover, and symbols (see [LSP](#lsp-language-server-protocol)) |
| `mcp` | Invoke MCP server tools |
| `todo_write` | Manage structured task lists |
| `write_memory` | Save a durable fact that should be true in every future session |
| `read_memory` | Look up durable facts saved by earlier sessions |
| `write_session_memory` | Append this session's log entry to `.solcode/solcode.md` |
| `read_session_memory` | Fuzzy-search or list recent session memories |
| `ask_user` | Ask user questions in interactive dialogs |
| `task` | Spawn sub-agents for independent work |
| `skill` | Load and execute custom skills |

### Batch coding operations

`BatchTool` is the model-visible entry point for View, Grep, Glob, LS, LSP,
Edit, MultiEdit, Write, MultiWrite, Patch and Bash. Its schema includes each
child's real input fields; children still use their normal permissions and hooks.
The builtin lossy compressor defers to BatchTool for its children and never
rewrites the JSON envelope; user-defined post-tool hooks still run.

```json
{
  "operations": [
    {"tool": "View", "input": {"path": "internal/tool/batch.go", "offset": 0, "limit": 100}},
    {"tool": "Grep", "input": {"pattern": "BatchTool", "path": "internal/engine"}}
  ]
}
```

- IDs are optional and default to `op_1`, `op_2`, etc. Use explicit unique IDs
  with `depends_on`. Dependencies order execution, **not** result interpolation:
  read unknown content first, then build exact replacements in a later call.
- Read-only batches are allowed in plan mode. An agent allowlist containing
  wrapped tools (e.g. `View`, `Grep`) automatically permits the BatchTool
  container, but never additional children. Listing only `BatchTool` does not
  grant permission to its underlying tools.
- Independent safe reads run with bounded concurrency, even if the batch has
  more reads than the parallelism limit. Mutations run sequentially.
- Default `output_mode: "compact"` returns short receipts for Edit/Write/Patch
  instead of repeating complete diffs. Read/search/Bash results remain available
  within the text budget. `output_mode: "full"` retains diffs but **does not**
  disable the limits. Long output keeps its beginning and end.
- Summarized/truncated results include `full_output_path` for targeted View
  reads using `offset`/`limit`. Local archives are private temporary files, not
  repository edits; their lifetime depends on system temp-directory cleanup.
  ACP archives use the client's filesystem when it supports both read and write.
  If archival fails, `output_storage_error` explicitly reports that full output
  is unavailable; no unusable archive path is returned.
- MultiEdit is **atomic per file by default**: every replacement in a given file
  must validate before that file changes, but a failed file no longer prevents
  unrelated files from being edited. Its error result lists failed files and
  retained successes. Retry only failed files. Use `MultiEdit.input.atomic: true`
  for all-or-nothing editing across files.
- `BatchTool.atomic: true` provides an all-or-nothing transaction for file tools
  only. Ordered operations may edit the same file repeatedly; it is snapshotted
  once and restored to its original state on failure.

Configure the bounds in settings (zero/unset size limits use defaults):

```json
{
  "batch": {
    "max_operations": 12,
    "max_parallel": 4,
    "max_result_runes": 18000,
    "max_total_result_runes": 24000,
    "max_read_retries": 1
  }
}
```

The aggregate budget covers combined result/error text, excluding JSON metadata,
and counts Unicode characters rather than tokens. Only transient safe read
failures are retried; writes and exact-match failures are never replayed automatically.

## Checkpoints & Rewind

solcode snapshots workspace files so you can restore code to an earlier turn.

- Before `Edit` / `Write` / `Patch` / `MultiEdit` / `MultiWrite` run, the runtime records the file's turn-start contents.
- `Bash` mutates files without declaring paths, so it uses before/after SHA-256 fingerprints instead: the workdir is hashed before and after the call, and only files whose hash or existence changed are captured (new files recorded as absent, so rewind deletes them). Walks are capped at **2000 files / 1 MiB each** and skip `.git`, `node_modules`, `vendor`, build dirs, hidden paths, and binaries.
- A checkpoint is created per user prompt (not per tool call). Turns are numbered from `0` and the newest 50 are retained.

| Command | Effect |
|---------|--------|
| `/checkpoints` | List turns with time, file count, and optional label |
| `/checkpoint-name <name> [turn]` | Label a checkpoint (omit `turn` for the newest); names are unique per session, case-insensitive |
| `/rewind <turn\|name>` | Restore workspace files to the start of that turn |

**Rewind restores code only** — conversation history, session state, and model context are untouched. Only files captured by a checkpoint are restored; skipped or never-touched files keep their current contents. The same commands are available in ACP mode.

## Memory

Two memory layers exist, and they are not interchangeable:

| Layer | Tools | Purpose | Storage |
|-------|-------|---------|---------|
| Durable facts | `WriteMemory` / `ReadMemory` | Preferences, project rules, verified commands, settled decisions — knowledge that should hold in every future session | Global memory store; relevant entries are injected automatically. Requires `memory.enabled` |
| Session log | `WriteSessionMemory` / `ReadSessionMemory` | The chronological record of what a session did, decided, and left unfinished | `<project>/.solcode/solcode.md` |

Pick by intent: "what happened in this session" → session memory; "a fact worth knowing in every future session" → `WriteMemory`. Most sessions write one session memory and zero to three `WriteMemory` entries.

`WriteSessionMemory` takes `keywords`, `summary`, and `importance`; the runtime appends the checkpoint turn, the files changed this session, the timestamp, and the session id. `ReadSessionMemory` fuzzy-searches by keyword, or returns the most recent entries when called without a query.

## Dynamic context scheduling

Dynamic context is materialized as tagged conversation messages at model-call
boundaries, after pending tool results. Unchanged mode, selected skill, folded-tool
catalog, and bootstrap memory/knowledge sections are not reinserted on every
round. Updates append to the transcript instead of moving earlier messages; the
stable system prefix remains unchanged. Session reload recognizes these tags,
without displaying internal overlays as user messages or counting them as new
user turns during compaction.
Leaving plan mode explicitly supersedes the earlier plan instructions.

The local **WorkingSet** is not dumped after every tool call, and a goal-only set
is not injected at startup. File/hash/fact metadata is supplied when a queued
user request changes the task, after context compaction, or alongside an anti-loop
signal. Optional Jev routing is consulted only for useful task-change snapshots;
compaction recovery and efficiency reminders do not require an extra model call.
Recovery restores metadata, not cached source code, so necessary targeted reads
remain allowed.

**Efficiency notes** are independent of task routing. Repeated identical reads,
searches, and errors can trigger reminders; changed counters are coalesced with a
four-round cooldown, while new signal types/targets can surface immediately.
Each reminder has bounded reference lists. Different
read windows/search options are distinct operations. Successful edits invalidate
related read/search repetition counts; queued tasks and compaction reset the
tracking window. Background completion notifications do not themselves count as
a new user task.

## Permission Modes

| Mode | Behavior |
|------|----------|
| `auto` | Ask for destructive operations, auto-approve reads |
| `accept_edits` | Auto-approve file edits, ask for other destructive tools |
| `bypass` | Skip permission prompts; `Task` and built-in sub-agents can be enabled in settings (disabled by default) |
| `plan` | Read-only tools plus `TodoWrite` and `ModeSwitch`; no `Task` or sub-agents |
| `goal` | Work from `goal.md` until complete; `Task` and sub-agents remain disabled |
| `ultra` | Bypass permissions and enable `Task`, sub-agents, and task workflows |

Switch permission modes at runtime with `Shift+Tab`; start a goal workflow through `/goal`.
ACP advertises all six modes in `session/new` and `session/load` and supports switching
through `session/set_mode`. Mode changes are preserved when switching models or reloading features.

Legacy `ultrl` settings and requests are accepted as an alias of `ultra`; mode lists,
current-mode updates, and saved runtime settings use the canonical `ultra` name.
`yolo` remains an alias of `bypass`.

Enable **Settings → Features → Bypass sub-agents**, or set
`"permissions": {"bypass_subagents": true}` in your settings file, to allow the
built-in explorer, worker, and reviewer roles in bypass. Turning this off hides
`Task` and blocks both `Task` and internal `Subagent` calls, including allowlisted
calls. Ultra retains sub-agent access regardless of this toggle; task workflows
still require ultra.

In **plan** mode, planning instructions are supplied through dynamic context. Mutating
tools (`Edit` / `Write` / `Patch` / …) are blocked; explore with read-only tools or
read-only `BatchTool` children and use `TodoWrite` to track the plan. Model-initiated
`ModeSwitch` transitions require user approval; entering `bypass`, `goal`, or `ultra`
also requires the current mode to be `plan`. Direct user mode selection in the TUI
or ACP does not use this model-initiated gateway.

## Project Structure

```
solcode/
├── cmd/solcode/main.go       # Entry point
├── internal/
│   ├── acp/                   # Agent Client Protocol stdio server (JSON-RPC)
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
│   ├── checkpoint/           # Turn-scoped file snapshots (code-only rewind)
│   ├── sessionmemory/        # Session log written to .solcode/solcode.md
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
## Local model files

Place Kev, EmbeddingGemma, and the organizer's GGUF files in ~/.solcode/models.
See the local GGUF configuration above; ONNX artifacts are not used by the app.
