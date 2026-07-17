#!/usr/bin/env node
/**
 * UserPromptSubmit — prefix every user prompt with a project reminder
 *
 * {
 *   "hooks": {
 *     "events": {
 *       "UserPromptSubmit": [{
 *         "matcher": "*",
 *         "hooks": [{
 *           "type": "command",
 *           "command": "node examples/hooks/user-prompt-prefix.mjs",
 *           "timeout_ms": 3000,
 *           "fail_mode": "open"
 *         }]
 *       }]
 *     }
 *   }
 * }
 *
 * Note: matcher for UserPromptSubmit is matched against the prompt string
 * (exact segment match via "|"), so prefer matcher "*" here.
 */

import { readEvent, allow, modifyPrompt } from "./lib/read-event.mjs";

const PREFIX =
  process.env.SOLCODE_PROMPT_PREFIX ||
  "[project note: prefer small diffs; run tests after edits]\n\n";

const event = await readEvent();
const prompt = String(event.prompt || "");

if (!prompt || prompt.startsWith(PREFIX.trim())) {
  allow();
  process.exit(0);
}

modifyPrompt(PREFIX + prompt, "prefixed user prompt");
