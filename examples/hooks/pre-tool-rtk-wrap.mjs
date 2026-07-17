#!/usr/bin/env node
/**
 * PreToolUse — rewrite Bash through an "rtk"-style wrapper
 *
 * Demo of modified_input: prefixes the shell command so a local compressor /
 * recorder CLI can wrap execution (replace "rtk" with your real binary).
 *
 * {
 *   "hooks": {
 *     "events": {
 *       "PreToolUse": [{
 *         "matcher": "Bash",
 *         "hooks": [{
 *           "type": "command",
 *           "command": "node examples/hooks/pre-tool-rtk-wrap.mjs",
 *           "timeout_ms": 3000,
 *           "fail_mode": "open"
 *         }]
 *       }]
 *     }
 *   }
 * }
 */

import { readEvent, allow, modifyInput } from "./lib/read-event.mjs";

const WRAPPER = process.env.SOLCODE_RTK_BIN || "rtk";

const event = await readEvent();
const input = { ...(event.tool_input || {}) };
const command = String(input.command || "").trim();

if (!command) {
  allow();
  process.exit(0);
}

// Already wrapped?
if (command === WRAPPER || command.startsWith(WRAPPER + " ")) {
  allow("already wrapped");
  process.exit(0);
}

// Skip interactive / long-running shells
if (/^(bash|sh|zsh|powershell|pwsh|cmd)(\s|$)/i.test(command)) {
  allow("skip interactive shell");
  process.exit(0);
}

input.command = `${WRAPPER} ${command}`;
modifyInput(input, `wrapped via ${WRAPPER}`);
