#!/usr/bin/env node
/**
 * PostToolUse — hard-trim oversized tool results (simple demo)
 *
 * Builtin compress_tool_result (headroom) is smarter; this shows how a
 * command hook can still rewrite modified_result itself.
 *
 * {
 *   "hooks": {
 *     "events": {
 *       "PostToolUse": [{
 *         "matcher": "Bash|View|Grep",
 *         "hooks": [{
 *           "type": "command",
 *           "command": "node examples/hooks/post-tool-trim.mjs",
 *           "timeout_ms": 3000,
 *           "fail_mode": "open"
 *         }]
 *       }]
 *     }
 *   }
 * }
 */

import { readEvent, allow, modifyResult } from "./lib/read-event.mjs";

const MAX_CHARS = Number(process.env.SOLCODE_HOOK_TRIM_CHARS || 12_000);

const event = await readEvent();
const block = event.tool_result;

if (!block || typeof block !== "object") {
  allow();
  process.exit(0);
}

// Never touch images / errors
if (block.type === "image" || block.is_error) {
  allow();
  process.exit(0);
}

const text = typeof block.text === "string" ? block.text : "";
if (text.length <= MAX_CHARS) {
  allow();
  process.exit(0);
}

const head = Math.floor(MAX_CHARS * 0.7);
const tail = MAX_CHARS - head - 80;
const trimmed =
  text.slice(0, head) +
  `\n\n... [post-tool-trim: omitted ${text.length - MAX_CHARS} chars] ...\n\n` +
  text.slice(-Math.max(tail, 0));

modifyResult(
  {
    type: "text",
    text: trimmed,
    is_error: Boolean(block.is_error),
  },
  `trimmed tool result ${text.length}→${trimmed.length} chars`,
);
