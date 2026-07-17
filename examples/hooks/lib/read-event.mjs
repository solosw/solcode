/**
 * Shared helpers for solcode command hooks (Node.js).
 *
 * Protocol:
 *   stdin  → one JSON object (hook.Event)
 *   stdout → one JSON object (hook.Result), or empty = allow
 *   stderr → free-form logs (ignored by solcode)
 *
 * Event fields (common):
 *   event, session_id, work_dir, tool_name, tool_input, tool_result, prompt, ...
 *
 * Result fields:
 *   decision: "allow" | "modify" | "block"
 *   modified_prompt, modified_input, modified_result, message
 */

import { stdin } from "node:process";

export async function readEvent() {
  const chunks = [];
  for await (const chunk of stdin) {
    chunks.push(chunk);
  }
  const raw = Buffer.concat(chunks).toString("utf8").trim();
  if (!raw) {
    return {};
  }
  return JSON.parse(raw);
}

export function reply(result) {
  // Only stdout is parsed; keep a single JSON object.
  process.stdout.write(JSON.stringify(result ?? { decision: "allow" }));
}

export function allow(message) {
  reply(message ? { decision: "allow", message } : { decision: "allow" });
}

export function block(message) {
  reply({ decision: "block", message: message || "blocked by hook" });
}

export function modifyInput(input, message) {
  reply({
    decision: "modify",
    modified_input: input,
    message: message || "input modified by hook",
  });
}

export function modifyResult(result, message) {
  reply({
    decision: "modify",
    modified_result: result,
    message: message || "result modified by hook",
  });
}

export function modifyPrompt(prompt, message) {
  reply({
    decision: "modify",
    modified_prompt: prompt,
    message: message || "prompt modified by hook",
  });
}
