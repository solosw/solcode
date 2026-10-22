# Solcode ACP v1 session selectors

Solcode implements **ACP v1** Session Config Options. This document describes
project behavior for the bundled desktop client and external clients such as
BaoCode. No BaoCode sources are part of this repository, and no ACP v2 adapter is
implemented here.

## Wire contract

- `session/new` and successful `session/load` return a complete `configOptions`
  array, alongside legacy `modes` metadata.
- Option objects use v1 `id`, `name`, `category`, `type: "select"`,
  `currentValue`, and `options`. Categories are semantic hints, not RPC IDs.
- `session/set_config_option` accepts `sessionId`, `configId`, and a string
  `value`. Use the exact option ID and value returned by the Agent.
- Success returns **all** options under `result.configOptions`. A subsequent
  model switch may remove `reasoning_effort` entirely; replace, never merge.
- Solcode also emits the complete snapshot via `session/update`, with
  `update.sessionUpdate: "config_option_update"` and `update.configOptions`.
  It emits a snapshot for successful client changes, including same-value
  requests, as well as autonomous changes and ACP slash-command pickers.
- Invalid option IDs, values, types, or sessions use `-32602`. Local runtime
  preflight/persistence failures use `-32603` with an explanation. They do not
  commit the candidate or pretend to be successful. Endpoint preflight is not a
  provider health check; an endpoint can still fail when actually called.
- Legacy `session/set_mode` normalizes old mode spellings, uses the same internal
  setter, and synchronizes both legacy mode and full-config notifications.
  New select values must use advertised canonical IDs.
- v2 `type: "id"` mutations are not accepted in a v1 connection.

Example request:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "session/set_config_option",
  "params": {
    "sessionId": "<session-id>",
    "configId": "model",
    "value": "provider-b/shared"
  }
}
```

## Options and capability declarations

| Solcode configuration ID | Behavior |
| --- | --- |
| `mode` | Auto, Accept Edits, Bypass, Plan, Goal, Ultra; the existing permission service remains authoritative |
| `model` | All configured providers/models, provider-qualified labels and opaque values; same-named models across providers stay distinct |
| `reasoning_effort` | Model-specific `supported_efforts`, or compatibility defaults when omitted; `[]` disables the selector |

A value currently encodes the escaped provider name and escaped model alias,
separated by `/`. Clients **must not parse or construct this representation**;
use Agent-published values. Credentials and endpoint secrets are never included.

The reasoning selector uses `id: "reasoning_effort"` and
`category: "thought_level"`, matching codex-acp. The category only tells the
client where to render the control; send the published ID when changing it:

```json
{
  "method": "session/set_config_option",
  "params": {
    "sessionId": "current-session-id",
    "configId": "reasoning_effort",
    "value": "high"
  }
}
```

The old Solcode `configId: "thought_level"` is still accepted as a compatibility
alias. Both IDs use the same validation and commit logic and return the complete
snapshot with only the canonical ID; no duplicate selectors are published.

Declare `supported_efforts` on a provider's model (or on the top-level settings
for a legacy single-model endpoint). Use only levels verified for that endpoint:

```json
{
  "name": "shared",
  "id": "your-model-id",
  "supported_efforts": ["low", "medium", "high"],
  "effort": "high"
}
```

This is an illustrative declaration, not a claim about any particular model.
Omitted/`null` capabilities now use **compatibility defaults** when thinking is
enabled. Older Solcode versions hid the entire ACP selector in this case, even
when `effort` was already configured; that made existing settings unusable from
BaoCode. The defaults are project conventions, **not an assertion that an
upstream supports every value**, and the ACP selector description says so:

| API format | Compatibility choices |
| --- | --- |
| OpenAI Chat / Responses | `none`, `low`, `medium`, `high`, `xhigh`, `max` (BaoCode's native `ProviderModel.defaultEfforts`) |
| Anthropic Messages | `low`, `medium`, `high`, `max` (no implicit `none`/`xhigh` on this wire format) |

A custom configured default effort is retained among the compatibility choices.
An explicit `thinking: false` suppresses fallback choices; an explicit nonempty
`supported_efforts` list still takes precedence. An empty list explicitly
disables adjustable reasoning, clearing `effort` and `thinking` for that model. Nonempty lists must contain unique, trimmed, nonempty
values; a model's explicit default `effort` must belong to its list. On a model
switch, preserve a supported old level; otherwise use the target's supported
default, then `high` if supported, then the first declared level. Thinking
settings control inference effort, not disclosure of private chains of thought.

Effort values are opaque provider-defined strings, not a universal enum.
Published values, including `none`, `minimal`, or custom levels, are forwarded
unchanged. Explicit lists replace the fallback choices, rather than merging
with them. No mapping between `max` and `xhigh`
is inferred. This does not change the independent `thinking` setting.

Unlike codex-acp, Solcode calls provider HTTP APIs directly, not Codex App Server
`turn/start`. The confirmed session `effort` reaches the next main-agent call as:

| Provider API | Request field |
|---|---|
| Anthropic Messages | `output_config.effort` |
| OpenAI Chat Completions | `reasoning_effort` |
| OpenAI Responses | `reasoning.effort` |

These transport fields must not be confused with either the ACP category or the
Codex App Server `effort` parameter. Solcode has no generic App Server
`model/list` capability source. Existing settings with only `effort` now publish
compatibility choices without requiring manual migration. Set verified
`supported_efforts` when the endpoint has a smaller or different supported set;
a rejection by the actual provider at inference time is not prevented by ACP
validation against a compatibility list.

BaoCode's ACP client (`lib/kernel/acp/acp_kernel.dart`) places the selector by
`category`, sends the actual option ID, and replaces the complete snapshot on
responses/notifications. Its ACP path does **not** invent missing thought
choices. Its native model settings (`lib/models/model_provider.dart`) do provide
fallback choices. Solcode must publish its own options for BaoCode to show them;
renaming an ID alone cannot fix an omitted selector.

See `examples/settings/settings.acp-selectors.example.json` for a neutral
multi-provider fixture. Replace model IDs, endpoints, and capability lists with
verified values before use.

## State, persistence, and generation boundary

Selections are session-scoped. ACP pickers do not implicitly rewrite global
defaults. With persistent sessions, an atomically written
`<session-dir>/acp-config/<sha256-session-id>.json` stores only model value, mode,
and effort. It is separate from transcript saves so completion of an in-flight
prompt cannot overwrite newer confirmed settings. Disabled persistence means
settings last only as long as the live session.

Loading first verifies that the transcript exists, then restores/validates the
sidecar against the current provider catalog and capabilities. Invalid saved
choices fall back to currently valid defaults, with visible adjustment messages.
Corrupt/unreadable state is an explicit load error. User-visible transcript and
tool IDs survive a cross-provider switch. Opaque signed/encrypted reasoning
blocks are removed at the boundary because they cannot safely cross models.
Restored transcripts also invalidate opaque reasoning, since their provider
provenance cannot be verified; retrying an original transcript never re-enables
blocks already invalidated by a model switch.

**A call that has already captured its configuration is not restarted. The next
main-agent API invocation captures the latest confirmed provider/model/effort,
even within an ongoing prompt.** The engine refreshes immediately before Create
as well as at tool-turn boundaries, including after lengthy compaction/routing.
Existing child runs retain their model snapshot. Permission changes use the
existing live service; model mutation does not bypass tool authorization.
Ancillary App clients/configuration are reconciled when the prompt finishes.

## Client rules

1. Use categories only to position selectors; send their actual IDs. Respect
   Agent labels, option grouping, and ordering. Show the first option of each
   primary category; put extras/unknown categories in generic settings.
2. Ignore unsupported input types and remove controls absent from a replacement
   snapshot. Thinking-only configurations are valid independently of a model
   selector.
3. Serialize mutations. Before sending each queued mutation, revalidate it
   against the latest confirmed snapshot. Never optimistically mark it applied.
4. Apply response and notification snapshots synchronously in receive order,
   before asynchronous response handlers can reorder them. Duplicate snapshots
   must not echo another mutation.
5. Sending a prompt waits for all pending settings. Errors retain the last
   confirmed state and prompt text and require an explicit retry; do not silently
   send using old settings.
6. Switching sessions, restarting, or closing invalidates old queued operations
   and ignores old-session responses/notifications. New and legacy mode mutation
   paths must not be mixed when config options exist.

## Verification

```sh
go test ./internal/acp ./internal/config ./internal/engine
go test -race ./internal/acp ./internal/config ./internal/engine
cd desktop
npm test
npm run typecheck
npm run build:renderer
```

Regression coverage includes new/load snapshots; limited/absent reasoning;
provider-qualified duplicate names; old/new mode synchronization; rejected
values and v2 mutations; runtime/persistence failure rollback; saved-state
fallback notices; an actual in-prompt Anthropic-to-OpenAI HTTP transition
(credentials, model, effort, and retained visible history); compaction-boundary
refresh; queued failures/retry; full-snapshot replacement; receive ordering;
and stale-session cancellation. Live provider availability and an external
BaoCode UI require separate integration verification.
