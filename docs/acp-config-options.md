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
  model switch may remove `thought_level` entirely; replace, never merge.
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

| Solcode ID / category | Behavior |
| --- | --- |
| `mode` | Auto, Accept Edits, Bypass, Plan, Goal, Ultra; the existing permission service remains authoritative |
| `model` | All configured providers/models, provider-qualified labels and opaque values; same-named models across providers stay distinct |
| `thought_level` | Published only for the current model's nonempty, explicitly declared `supported_efforts` |

A value currently encodes the escaped provider name and escaped model alias,
separated by `/`. Clients **must not parse or construct this representation**;
use Agent-published values. Credentials and endpoint secrets are never included.

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
Omitted/`null` capabilities are unknown: no thinking selector is advertised.
An empty list explicitly disables adjustable reasoning, clearing `effort` and
`thinking` for that model. Nonempty lists must contain unique, trimmed, nonempty
values; a model's explicit default `effort` must belong to its list. On a model
switch, preserve a supported old level; otherwise use the target's supported
default, then `high` if supported, then the first declared level. Thinking
settings control inference effort, not disclosure of private chains of thought.

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
