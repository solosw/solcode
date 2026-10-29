# ACP subagent sessions

SolCode supports the standard subagent extension used by codex-acp over **ACP v1**.
It does not implement ACP v2 or AIR's private `subagent_spawned` protocol.

## Negotiation

The agent advertises `agentCapabilities.sessionCapabilities.subagents: {}` in
its initialize response. To receive child sessions, the client must send an
object-valued `clientCapabilities.subagents`:

```json
{"protocolVersion":1,"clientCapabilities":{"subagents":{}}}
```

Missing, null, boolean, or array values do not opt in. These clients (including
the current SolCode desktop client) continue receiving the legacy aggregate
subagent tool cards in the root session. Child text/thinking never become root
assistant message chunks.

## Notifications

Before any child content, the parent receives a `session/update` notification:

```json
{"sessionId":"parent","update":{"sessionUpdate":"subagent_update","sessionId":"child","title":"Explore files","description":"Explore files","capabilities":{}}}
```

Text, thoughts, tool starts/results (including diffs and locations), and
`TodoWrite` plans then use the child ID in the notification's outer
`sessionId`. Tool calls carry their own IDs, scoped to the execution run.
Parallel agents do not share tool state or text streams.

Foreground work state is sent to the **parent**, not as a v2-only
`state_update` on the child:

```json
{"sessionId":"parent","update":{"sessionUpdate":"subagent_update","sessionId":"child","state":{"state":"running"}}}
{"sessionId":"parent","update":{"sessionUpdate":"subagent_update","sessionId":"child","state":{"state":"idle","stopReason":"end_turn"}}}
```

Failures use `stopReason: "error"` and an `error` object; cancellation uses
`stopReason: "cancelled"`. Finishing a run does not close the conversation:
resuming the same agent reuses its child session ID and reports running again.
Detached agents can continue sending updates after the root prompt response.
Duplicate terminal events and events from superseded runs are ignored.

## Scope

- Child sessions are notification-only. Empty child capabilities intentionally
  advertise no independent prompt, cancel, or close support.
- Permission, client filesystem, and [client terminal](acp-terminals.md) requests
  remain attached to the root session. Terminal content references are routed
  to the owning child tool card.
- Child associations are live, in-process state; `session/load` does not replay
  historical child sessions or restore their ACP IDs across agent restarts.
- Delegation policy is unchanged: this does not enable nested subagent creation.
