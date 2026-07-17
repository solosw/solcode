# solcode hooks — multi-language examples

Command hooks receive a JSON **event** on stdin and print a JSON **result**
on stdout (or print nothing = allow).

```
stdin  → { "event": "PreToolUse", "tool_name": "Bash", "tool_input": {...}, ... }
stdout → { "decision": "allow" | "modify" | "block", ... }
```

stderr is free-form logging (ignored by solcode). Do **not** write extra
stdout lines—only one JSON object.

Commands run via the host shell (`bash -c` on Unix, `cmd /c` on Windows) with
`cwd = work_dir`. Relative paths work when you start solcode from the repo root.

## Events

| Event | Typical use | Useful result fields |
|-------|-------------|----------------------|
| `UserPromptSubmit` | Prefix / rewrite user prompt | `modified_prompt` |
| `PreToolUse` | Guard or rewrite tool input | `modified_input`, `block` |
| `PostToolUse` | Log / trim / compress results | `modified_result` |
| `Notification` | Side effects | `allow` |
| `Stop` | Cleanup | `allow` |

Matchers: `"*"` or exact tool name (or prompt segment for `UserPromptSubmit`).
Multiple names: `"Bash|View|Grep"`.

Hook types:

- `command` — shell command string
- `builtin` — e.g. `compress_tool_result` (default headroom compressor)

`fail_mode`: `"open"` continues on hook errors; anything else aborts the chain.

## Languages in this folder

| Language | Path | Notes |
|----------|------|--------|
| **Node** | `*.mjs`, `lib/read-event.mjs` | Default samples; Node 18+ |
| **Python** | `python/` | 3.10+; stdlib only |
| **Bash** | `bash/` | Prefer `jq` when available |
| **PowerShell** | `powershell/` | Windows-friendly; use `-File` |
| **Go** | `go/bash_guard`, `go/prompt_prefix` | `go run` or build a binary |

### Node (default)

| Script | Event |
|--------|--------|
| `pre-tool-bash-guard.mjs` | PreToolUse / Bash |
| `pre-tool-rtk-wrap.mjs` | PreToolUse / Bash (`modified_input`) |
| `post-tool-log.mjs` | PostToolUse / `*` |
| `post-tool-trim.mjs` | PostToolUse / Bash\|View\|Grep |
| `user-prompt-prefix.mjs` | UserPromptSubmit |

Dry-run:

```bash
node examples/hooks/pre-tool-bash-guard.mjs < examples/hooks/_sample-block.json
# → decision block

node examples/hooks/pre-tool-rtk-wrap.mjs < examples/hooks/_sample-wrap.json
# → decision modify, command prefixed with rtk
```

### Python

```bash
python examples/hooks/python/pre_tool_bash_guard.py < examples/hooks/_sample-block.json
python examples/hooks/python/user_prompt_prefix.py <<'EOF'
{"event":"UserPromptSubmit","prompt":"hello"}
EOF
```

settings `command` examples:

```text
python examples/hooks/python/pre_tool_bash_guard.py
python examples/hooks/python/post_tool_trim.py
python examples/hooks/python/post_tool_log.py
python examples/hooks/python/user_prompt_prefix.py
```

### Bash

```bash
bash examples/hooks/bash/pre-tool-bash-guard.sh < examples/hooks/_sample-block.json
```

### PowerShell (Windows)

```powershell
Get-Content examples/hooks/_sample-block.json -Raw |
  powershell -NoProfile -ExecutionPolicy Bypass -File examples/hooks/powershell/pre-tool-bash-guard.ps1
```

settings `command` (escaped for JSON):

```text
powershell -NoProfile -ExecutionPolicy Bypass -File examples/hooks/powershell/pre-tool-bash-guard.ps1
```

### Go

```bash
# dry-run
go run ./examples/hooks/go/bash_guard < examples/hooks/_sample-block.json

# lower latency binary
go build -o bin/bash-guard ./examples/hooks/go/bash_guard
# command: bin/bash-guard   (or .\bin\bash-guard.exe)
```

`go run` cold-starts every tool call—build a binary for real use.

## settings.json

Merge [`settings.hooks.example.json`](settings.hooks.example.json) into your
config. Full stack sample: [`../settings/settings.full.example.json`](../settings/settings.full.example.json).

Node baseline:

```json
{
  "hooks": {
    "events": {
      "UserPromptSubmit": [
        {
          "matcher": "*",
          "hooks": [
            {
              "type": "command",
              "command": "node examples/hooks/user-prompt-prefix.mjs",
              "timeout_ms": 3000,
              "fail_mode": "open"
            }
          ]
        }
      ],
      "PreToolUse": [
        {
          "matcher": "Bash",
          "hooks": [
            {
              "type": "command",
              "command": "node examples/hooks/pre-tool-bash-guard.mjs",
              "timeout_ms": 5000,
              "fail_mode": "open"
            }
          ]
        }
      ],
      "PostToolUse": [
        {
          "matcher": "*",
          "hooks": [
            {
              "type": "builtin",
              "name": "compress_tool_result",
              "fail_mode": "open"
            }
          ]
        }
      ]
    }
  }
}
```

Language swap cheat-sheet lives under `_comment_language_swaps` in
`settings.hooks.example.json` (ignored by the Go JSON decoder).

### Disable default compression

```json
{
  "hooks": {
    "events": {
      "PostToolUse": [
        {
          "matcher": "*",
          "hooks": [
            { "type": "builtin", "name": "disable_compress_tool_result" }
          ]
        }
      ]
    }
  }
}
```

## Result schema

```json
{
  "decision": "allow | modify | block",
  "modified_prompt": "string",
  "modified_input": { },
  "modified_result": { "type": "text", "text": "...", "is_error": false },
  "message": "human-readable note",
  "suppress_output": false
}
```

## Env knobs used by samples

| Variable | Used by |
|----------|---------|
| `SOLCODE_PROMPT_PREFIX` | user-prompt-prefix (all languages) |
| `SOLCODE_HOOK_LOG` | post-tool-log path |
| `SOLCODE_HOOK_TRIM_CHARS` | post-tool-trim max length |
| `SOLCODE_RTK_BIN` | Node rtk-wrap binary name |
