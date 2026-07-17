#!/usr/bin/env bash
# PreToolUse — Bash safety guard
# command: bash examples/hooks/bash/pre-tool-bash-guard.sh
set -euo pipefail

event="$(cat)"
command="$(printf '%s' "$event" | sed -n 's/.*"command"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"

# Prefer jq when available
if command -v jq >/dev/null 2>&1; then
  command="$(printf '%s' "$event" | jq -r '.tool_input.command // empty')"
fi

deny() {
  printf '%s' "{\"decision\":\"block\",\"message\":\"Bash command blocked by pre-tool-bash-guard: $1\"}"
  exit 0
}

case "$command" in
  *curl*|*wget*|*Invoke-WebRequest*) deny "$command" ;;
esac

if printf '%s' "$command" | grep -Eiq 'rm[[:space:]]+(-[a-zA-Z]*f[a-zA-Z]*[[:space:]]+)?/'; then
  deny "$command"
fi
if printf '%s' "$command" | grep -Eiq 'git[[:space:]]+push.*--force'; then
  deny "$command"
fi
if printf '%s' "$command" | grep -Eiq 'drop[[:space:]]+database'; then
  deny "$command"
fi

printf '%s' '{"decision":"allow"}'
