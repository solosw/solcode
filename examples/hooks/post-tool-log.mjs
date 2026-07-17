#!/usr/bin/env node
/**
 * PostToolUse — append a JSONL audit log of tool calls
 *
 * Does not modify results (decision: allow). Useful next to the builtin
 * compress_tool_result hook.
 *
 * {
 *   "hooks": {
 *     "events": {
 *       "PostToolUse": [{
 *         "matcher": "*",
 *         "hooks": [{
 *           "type": "command",
 *           "command": "node examples/hooks/post-tool-log.mjs",
 *           "timeout_ms": 3000,
 *           "fail_mode": "open"
 *         }]
 *       }]
 *     }
 *   }
 * }
 *
 * Log file: $SOLCODE_HOOK_LOG or <work_dir>/.solcode/hooks-tool.jsonl
 */

import { appendFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { readEvent, allow } from "./lib/read-event.mjs";

const event = await readEvent();
const workDir = event.work_dir || process.cwd();
const logPath =
  process.env.SOLCODE_HOOK_LOG ||
  join(workDir, ".solcode", "hooks-tool.jsonl");

const result = event.tool_result || {};
const text =
  typeof result === "object" && result && typeof result.text === "string"
    ? result.text
    : "";

const line = {
  ts: new Date().toISOString(),
  event: event.event,
  tool: event.tool_name,
  session_id: event.session_id,
  is_error: Boolean(result.is_error),
  text_chars: text.length,
  text_preview: text.slice(0, 200),
  input: event.tool_input,
};

try {
  mkdirSync(dirname(logPath), { recursive: true });
  appendFileSync(logPath, JSON.stringify(line) + "\n", "utf8");
} catch (err) {
  // fail_mode open: still allow the tool result through
  console.error("post-tool-log write failed:", err.message);
}

allow();
