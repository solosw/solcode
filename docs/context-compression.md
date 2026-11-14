# AI-controlled context compression

SolCode uses a Go adaptation of the compression strategy from
[billion-context](https://github.com/ranxianglei/billion-context), including its
[acp-kernel](https://github.com/ranxianglei/acp-kernel) design. The reference is
billion-context commit `df0a4023bf864531592dd0f10b32f4fcee9423b6`, specifically
`kernel/src/compress.ts`, `prune.ts`, `decompress.ts` and `compress-tools.ts`.
Copyright (c) 2026 ranxianglei. The upstream MIT license and additional product
attribution terms are preserved verbatim in
[third_party/billion-context/LICENSE](../third_party/billion-context/LICENSE).

This is a native adaptation of the core strategy, not a dependency on the Node
proxy or a claim that all of its transport adapters and heuristics were ported.

## Who writes the summary

The current working AI chooses the range and writes the summary in the
`compress` call. The tool does not invoke a second model. Its work is deterministic:
validate references, protection rules, tool pairing and token reduction, then
commit a new summary block and active view. Older summaries can be included in a
new fold, which increments the hierarchy level and retains their child links.

The host offers four stable core tools to main and child agents:

| Tool | Purpose |
| --- | --- |
| `acp_status` | Compact overview/ranges with `view:"overview"` or `view:"ranges"`; detailed references with `view:"messages"` (default), paginated with offset/limit. |
| `compress` | Inclusive visible start/end references, a model-authored summary, optional topic, and the revision from a current reminder or status response. |
| `decompress` | Read a block's evidence or a source message; optionally restore a visible block into the active view. |
| `search_context` | Unified lookup of context originals/summaries, archival memory and the current session log. |

Example sequence:

```json
{"start":"m2","end":"m12","summary":"Implemented auth.go validation; targeted test passed. Integration test remains pending. Preserve the user's public API constraint.","topic":"Token validation","revision":0}
```

The example is the argument to `compress` using a current `context-budget`
reminder or `acp_status` response. A reminder already contains the revision, safe
ranges, protected ranges and existing summary-block metadata, so an extra status
call is optional. Refresh status when state is uncertain, the model changes,
references become stale, or other ranges/details are needed. References
are opaque and local to the current session; do not invent them or use a sibling's
references. `mN` identifies an immutable source message; `bN` identifies a fold.

## Unified search and recall

`search_context` replaces the model-visible `ReadMemory` and `ReadSessionMemory`
entry points. It defaults to `source:"all"`; select `"context"`, `"memory"` or
`"session_memory"` to query one source. Responses group results under `sources`
and include independent `ok`, `unavailable`, `skipped` or `error` status values.
A disabled or failed source does not discard results from the others.

Context lookup remains literal and case-insensitive, with stable `mN`/`bN`
references for `decompress`. Its `offset`/`next_offset` pagination is unchanged;
use `source:"context"` when continuing a page. Archival memory retains its
`kind`/`scope` filters, beliefs, other-session labels and cross-session opt-in.
The session log remains fuzzy-searchable and scoped to the current session id.
An empty query lists beliefs/recent memories and skips context lookup.
`limit` applies per source: defaults are 10/8/5 for context/memory/session log,
with caps of 50/25/25. Memory and session-log reads retain their previous bounded
lookup behavior without offset pagination. This is a per-source result bound,
not a merged similarity-ranked top-k: archival memory keeps its existing retrieval
ranking, session logs use fuzzy search, and context matches follow stored history
order. The two old read tools remain
registered for legacy execution, but are hidden from model discovery.

Examples:

```json
{"query":"build command"}
{"query":"token validation", "source":"context", "offset":0, "limit":10}
{"source":"memory", "kind":"workflow", "scope":"project", "limit":8}
{"query":"unfinished", "source":"session_memory", "limit":5}
```

## Preservation, recovery and limits

Source messages, fold child links, monotonically allocated references and the
active view are persisted together in the session checkpoint. Originals are
not sent to the chat model while covered by a fold. They remain on disk; archive
size grows with history. This implementation keeps the archive in the session
snapshot rather than introducing a separate database or remote service.

Compression cannot remove the latest real user request, the latest runtime
policy/role/skill/context overlays, the two most recent messages, or legacy
handoff/project-knowledge messages. A range cannot split a tool call/result group.
A stale revision, invalid range, empty/non-reducing summary or failed save leaves
the active history unchanged. Host permissions continue to gate every tool call.

`decompress` reads by default. For `bN`, offset/limit count child messages; previews
are bounded and include source references. For `mN`, offset/limit count Unicode
characters in the exact serialized message (default 4000, capped at 8000).
Follow `next_offset` until `total_runes` to retrieve large tool/image payloads.
`deep:true` traverses nested folds. `restore:true` replaces a visible fold with its
children, subject to both a conservative history budget and the composed request
budget. Reading is preferable when full restoration would exceed capacity.

Child forks retain only evidence reachable from their selected inherited view.
They get independent active state, retain the existing 80%/40% inheritance window,
and can read inherited fold originals after restart. They cannot use these tools
to access unrelated parent history or sibling sessions. Historical tool calls are
never replayed when restoring a block.

Existing sessions without compression state receive references on first use.
A legacy handoff stays readable as the summary already present; text discarded
by a previous software version cannot be reconstructed. New explicit handoffs
archive their replaced messages and expose a block through `acp_status`.
`ReadObservation` is hidden from model discovery and is not used by this workflow;
its old execution registration remains for compatibility.

## Context pressure and cache behavior

At each safe model boundary, SolCode estimates the composed input request,
including messages, system instructions and tool schemas. Its local adaptation
of the reference strategy uses the following default pressure bands and gates:

- Below 45%: no budget reminder.
- From 45% to below 75%: wait for both 50,000 tokens of growth and 15 model
  iterations since the baseline or last reminder.
- From 75% to below 95%: issue an initial pressure reminder when useful history
  is available. Later reminders require a five-iteration cooldown and at least
  `min(20,000, 45% of the context limit)` new tokens.
- At 95% and above: allow emergency escalation past the normal cooldown, growth
  and minimum-benefit gates. Repeated emergency reminders still require the
  five-iteration cooldown and positive growth.

Ordinary reminders require an estimated saving of at least 5,000 tokens. The
planning estimate retains a quarter of each eligible range plus a 128-token fold
wrapper; actual savings depend on the model-authored summary and are validated
by `compress`. Even an emergency reminder requires a structurally safe range
with positive estimated savings. A reminder that would itself exceed the input
budget is omitted without consuming the reminder cooldown.

Reminder state survives user turns and session checkpoints. A successful fold
resets the growth baseline and cooldown; child forks start their own schedule.
Changing the configured context limit resets the schedule for the new budget.
Reminders include the revision, model, request/history/system-and-tools token
estimates, safe inclusive range references, protected ranges and existing block
metadata. Lists are bounded to eight entries each, with full range counts; use
`acp_status` for more detail. Safety ranges preserve tool exchanges and current
protection rules. The working AI can choose completed work and write its own
summary through `compress`.

Settings can override the local policy in `context_nudge` (zero or omitted
fields use defaults):

```json
{
  "context_nudge": {
    "min_pressure": 0.45,
    "max_pressure": 0.75,
    "emergency_pressure": 0.95,
    "frequency": 5,
    "iteration_threshold": 15,
    "growth_tokens": 50000,
    "min_growth_tokens": 20000,
    "min_benefit_tokens": 5000
  }
}
```

This is a native policy using local token estimates, not a byte-for-byte port of
the proxy's provider-usage meters or tier promotion logic. Registering these
tools disables the old automatic history truncation path. At
`memory.compaction_trigger_percent` (default 85%) of the composed input budget,
the host requires the active main model to call `compress`. The request includes
a fresh revision and safe ranges, bypasses reminder cooldowns and uses the
provider's named `tool_choice` with parallel tool calls disabled. The engine also
validates the response: plain final text or another tool cannot satisfy this
request, and is reported as an explicit error without executing that response.

The mandatory call preserves the chat streaming setting, with thinking disabled.
Streamed tool arguments update compression status without appearing as chat text.
Each compression request has a five-minute deadline, and reserves output headroom
with an output cap of 8,192 tokens (or the lower configured limit). Timeout,
truncated output, and transport failures stop explicitly and preserve history.
The model writes the summary directly in the tool arguments. Only the normal
`compress` executor can commit the fold, after checkpointing its archive and active
view together. Failed tool calls can be corrected on up to three consecutive
attempts; invalid summaries, stale revisions and failed saves preserve history.
Successful compression continues the original task with its normal model settings
and turn budget. If the request remains above the trigger, compression is required
again before ordinary work. With no safe range, protected history stays intact;
at the hard composed-request limit, the run stops with an explicit error.

Explicit `/compact` uses the same forced tool path even below the threshold, then
returns after a successful fold. It leaves the latest real user request and
protected history in place, and skips when no safe range exists.

Tools and system prompts remain stable across folds. Source references are shown
in tool responses rather than rewritten onto every historical message. A fold
changes only the selected range; unchanged prefix/suffix messages and existing
summary bytes remain stable. Replacing a range can invalidate provider caching
from that position onward. This is not a guarantee of zero cache misses, nor a
benchmark claim about upstream savings.

## Session compression and memory extraction models

Automatic threshold compression and `/compact` force the current main model to
call `compress`, using its configured context/output limits and API client. They
do not create a separate summary-model request or use `fast_model` or
`memory.organizer`. A live model switch applies at the next model boundary.
The model can fold existing summaries into higher-level blocks through the same
tool. Compression requests retain streaming when chat streaming is enabled.

Background memory/session summaries and archival extraction continue to use
`memory.organizer`. Both local GGUF and independently configured API runtimes are
supported for this backend. Saving feature settings rebuilds the organizer runtime
so changes to its model, API endpoint and credentials apply without restarting.
Local memory organization never falls back to a network model.

The AI's `compress` tool continues to accept a summary supplied by the working
model without making an additional model request.
