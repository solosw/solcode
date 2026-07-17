#!/usr/bin/env bash
# UserPromptSubmit — prefix prompts
# command: bash examples/hooks/bash/user-prompt-prefix.sh
set -euo pipefail

PREFIX="${SOLCODE_PROMPT_PREFIX:-[project note: prefer small diffs; run tests after edits]
}"

event="$(cat)"
prompt=""
if command -v jq >/dev/null 2>&1; then
  prompt="$(printf '%s' "$event" | jq -r '.prompt // empty')"
else
  prompt="$(printf '%s' "$event" | sed -n 's/.*"prompt"[[:space:]]*:[[:space:]]*"\(.*\)".*/\1/p' | head -n1)"
fi

if [[ -z "$prompt" ]]; then
  printf '%s' '{"decision":"allow"}'
  exit 0
fi

# Already prefixed?
if [[ "$prompt" == "$(printf '%s' "$PREFIX" | head -n1)"* ]]; then
  printf '%s' '{"decision":"allow"}'
  exit 0
fi

if command -v jq >/dev/null 2>&1; then
  jq -nc --arg p "${PREFIX}${prompt}" '{decision:"modify",modified_prompt:$p,message:"prefixed user prompt"}'
else
  # Minimal JSON escape for common cases
  escaped="$(printf '%s' "${PREFIX}${prompt}" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g' | awk '{printf "%s\\n",$0}' | sed 's/\\n$//')"
  printf '{"decision":"modify","modified_prompt":"%s","message":"prefixed user prompt"}' "$escaped"
fi
