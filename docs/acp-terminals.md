# ACP v1 client terminals

When an ACP client advertises `clientCapabilities.terminal: true`, SolCode
executes Bash commands through the client's standard terminal methods:

```json
{"protocolVersion":1,"clientCapabilities":{"terminal":true}}
```

Clients without that capability keep the existing local Bash execution path.
There is no Codex `asyncTasks` dependency or private terminal extension.

## Lifecycle

1. After normal agent-side permissions, hooks, and command validation, call
   `terminal/create` with the root session ID, absolute working directory,
   output byte limit, and shell program/arguments. SolCode uses `bash -c` on
   Unix and `cmd /c` on Windows, matching its local Bash tool.
2. Attach the returned ID to the owning tool card as terminal content:

   ```json
   {"sessionId":"session","update":{"sessionUpdate":"tool_call_update","toolCallId":"call","content":[{"type":"terminal","terminalId":"terminal"}]}}
   ```

3. Use `terminal/wait_for_exit` to await completion. The client displays live
   terminal output while the call runs; the agent does not poll output.
4. Retrieve the final combined output through `terminal/output`, including
   exit status and any truncation indicator, for the model's tool result.
5. Always attempt `terminal/release` once the terminal is no longer needed.
   Terminal references remain on the completed tool card so the client can
   retain the output after release.

Cancellation, a hard command deadline, or a wait RPC error triggers
`terminal/kill` before final output retrieval and release. Cleanup RPCs use
short, independently bounded contexts rather than the canceled prompt context.
A canceled create request retains its pending reply: if the client later
returns a terminal ID, SolCode attempts to kill and release it even though the
prompt has already returned. Client errors never cause an automatic local
retry, which could otherwise execute the same command twice.

## Background work and tool ownership

- The existing engine background scheduler still controls when a main-agent
  Bash call yields, including `run_in_background`. The terminal keeps running
  while the model performs independent work. Its completion notification is
  delivered through the existing scheduler, not a new background-task protocol.
- `timeout` remains the command's hard deadline, not the foreground yielding
  interval. Long-timeout calls use the same client terminal lifecycle rather
  than starting a second process in the local background-job registry.
- Bash calls inside BatchTool share their parent card. Multiple terminal
  references accumulate and are preserved in the final update.
- Subagents use the same capability. With standard subagent sessions enabled,
  their terminal references appear on the child tool card; legacy clients see
  them on the aggregate subagent card. Terminal RPCs retain the root session ID
  for client authorization, like client filesystem requests.
- Model switches and feature reloads preserve the negotiated terminal runner.

## Security and limits

- The client owns execution, process management, and live output rendering. Its
  own permission checks still apply, even in an agent mode that skips approval.
- If SolCode's local sandbox is enabled, Bash stays on the local sandbox path;
  the client terminal API cannot guarantee that sandbox's restrictions.
- The client must provide the requested shell and interpret `command` plus
  `args` as an executable/argv pair, not shell-parse the command a second time.
- Disconnects do not promise process survival or cleanup: after transport loss,
  SolCode cannot send kill/release, and the client owns remaining resources.
- This is **agent-side** terminal support. SolCode's current desktop client does
  not advertise `terminal: true` or implement a terminal manager; it continues
  using local agent execution. Other clients must implement all five standard
  methods before advertising the capability.

Reference: [ACP v1 Terminals](https://agentclientprotocol.com/protocol/v1/terminals.md).
