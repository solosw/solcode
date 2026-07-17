#!/usr/bin/env bash
# PostToolUse — append a one-line audit log
# command: bash examples/hooks/bash/post-tool-log.sh
set -euo pipefail

event="$(cat)"
work_dir="$(pwd)"
if command -v jq >/dev/null 2>&1; then
  work_dir="$(printf '%s' "$event" | jq -r '.work_dir // empty')"
  [[ -z "$work_dir" ]] && work_dir="$(pwd)"
fi

log_path="${SOLCODE_HOOK_LOG:-$work_dir/.solcode/hooks-tool.jsonl}"
mkdir -p "$(dirname "$log_path")"

# Keep a compact line; full event may be large.
ts="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date)"
tool=""
if command -v jq >/dev/null 2>&1; then
  tool="$(printf '%s' "$event" | jq -r '.tool_name // empty')"
  printf '%s\n' "$(printf '%s' "$event" | jq -c --arg ts "$ts" '{ts:$ts,event:.event,tool:.tool_name,session_id:.session_id,input:.tool_input}')" >>"$log_path"
else
  printf '{"ts":"%s","tool":"%s","raw":true}\n' "$ts" "$tool" >>"$log_path"
fi

printf '%s' '{"decision":"allow"}'
