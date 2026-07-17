#!/usr/bin/env node
/**
 * PreToolUse — Bash safety guard
 *
 * Blocks obvious destructive / network commands. Allows everything else.
 *
 * Config (settings.json):
 * {
 *   "hooks": {
 *     "events": {
 *       "PreToolUse": [{
 *         "matcher": "Bash",
 *         "hooks": [{
 *           "type": "command",
 *           "command": "node examples/hooks/pre-tool-bash-guard.mjs",
 *           "timeout_ms": 5000,
 *           "fail_mode": "open"
 *         }]
 *       }]
 *     }
 *   }
 * }
 */

import { readEvent, allow, block } from "./lib/read-event.mjs";

const DENY = [
  /\brm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?\/\b/,
  /\bformat\s+[a-z]:/i,
  /\b(curl|wget|Invoke-WebRequest)\b/i,
  /\bgit\s+push\s+.*--force\b/i,
  /\bdrop\s+database\b/i,
];

const event = await readEvent();
const input = event.tool_input || {};
const command = String(input.command || "");

for (const re of DENY) {
  if (re.test(command)) {
    block(`Bash command blocked by pre-tool-bash-guard: ${command}`);
    process.exit(0);
  }
}

allow();
