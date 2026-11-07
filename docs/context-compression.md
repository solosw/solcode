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
| `acp_status` | Context tokens, revision, stable references, protected messages and safe boundaries; paginated with offset/limit. |
| `compress` | Inclusive visible start/end references, a model-authored summary, optional topic, and the status revision. |
| `decompress` | Read a block's evidence or a source message; optionally restore a visible block into the active view. |
| `search_context` | Unified lookup of context originals/summaries, archival memory and the current session log. |

Example sequence:

```json
{"start":"m2","end":"m12","summary":"Implemented auth.go validation; targeted test passed. Integration test remains pending. Preserve the user's public API constraint.","topic":"Token validation","revision":0}
```

The example is the argument to `compress` after checking `acp_status`. References
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

At approximately 75% of the message budget, the loop appends a compression reminder
at a safe model boundary, with an eight-turn cooldown. The model chooses what to
fold. Registering these tools disables the old automatic history truncation path.
At the hard composed-request limit, SolCode stops before issuing an oversized
request and preserves history; explicit `/compact` is available when a summary
backend is configured. A single unexpectedly large tool result can still reach
this limit before the AI gets another turn.

Tools and system prompts remain stable across folds. Source references are shown
in tool responses rather than rewritten onto every historical message. A fold
changes only the selected range; unchanged prefix/suffix messages and existing
summary bytes remain stable. Replacing a range can invalidate provider caching
from that position onward. This is not a guarantee of zero cache misses, nor a
benchmark claim about upstream savings.

## Shared model for explicit summaries and memory extraction

`/compact` and background session summary generation use only
`memory.organizer`, the same configured runtime/model/API endpoint as memory
organization and extraction. There is no separate session-summary model setting
and no fallback to `fast_model` or the chat provider. `fast_model` retains its other
existing purposes.

Both local GGUF and independently configured API runtimes are supported. The
existing organizer enable switch controls this backend. Without it, explicit
summary generation reports a configuration error and retains history; the AI's
`compress` tool still works because its summary is supplied as an argument.

Long explicit-summary inputs are partitioned on UTF-8 boundaries and summarized
hierarchically within the organizer's context/output limits. Empty, truncated or
non-reducing generation fails safely. Local mode never falls back to a network
model. Explicit handoff summaries and archival memory extraction have separate
prompts/output validation while sharing the inference backend and configuration.
