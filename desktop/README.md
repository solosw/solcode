# Solcode Desktop

An Electron desktop front end for the [solcode](../) coding agent. It talks to
`solcode --acp` over the [Agent Client Protocol](https://agentclientprotocol.com)
and renders the session as a Codex-style workspace: sidebar, streaming
transcript, tool cards with inline diffs, and a permission-mode switcher.

The app owns no agent logic. solcode remains the agent; this is a client.

## Architecture

```
Electron main (src/main/main.ts)
  ├─ spawn `solcode --acp`            → child process, stdio
  ├─ AcpClient (src/main/acp-client.ts)  newline-delimited JSON-RPC framing
  ├─ AgentSession (src/main/agent-session.ts)  updates → transcript state
  └─ BrowserWindow
        └─ preload.cjs (contextBridge)  → window.solcode
              └─ React renderer (src/renderer/)
```

Three files carry the integration:

| File | Responsibility |
|---|---|
| `src/main/acp-client.ts` | Framing, request/response correlation, permission replies |
| `src/main/agent-session.ts` | Turns raw `session/update` notifications into transcript state |
| `src/shared/protocol.ts` | Wire types mirroring solcode's `internal/acp/protocol.go` |

### Protocol notes

These are load-bearing details verified against the real binary:

- **Framing is newline-delimited JSON.** One JSON-RPC object per line, no
  `Content-Length` header (`internal/acp/conn.go`). A partial read must be
  buffered until a newline arrives.
- **Chunk text is nested.** `agent_message_chunk` and `agent_thought_chunk`
  carry text in `content: { type: 'text', text }`, not a top-level `text` field.
- **Usage is flattened.** `used` and `size` sit directly on the update object,
  not under a `usage` key.
- **Unknown server-initiated requests must be answered.** A request with an id
  that is never replied to blocks the agent. Unrecognised methods get `-32601`.
- **The renderer advertises no filesystem capability.** solcode then uses the
  working directory it was given instead of routing reads/writes back through
  the client.

## Development

Requires Node 22+ and a `solcode` binary.

```sh
npm install
npm run build     # tsc (main) + vite (renderer)
npm start         # build, then launch Electron
```

Point the app at a specific binary with `SOLCODE_BIN=/path/to/solcode`. Otherwise
it searches `process.resourcesPath`, the project directory, and `PATH`.

### Verification

```sh
npm run typecheck                        # both tsconfigs
node scripts/integration-check.mjs       # ACP protocol, headless
node scripts/window-check.mjs            # full stack over CDP, drives a real turn
node scripts/screenshot.mjs              # writes .verify/window.png
```

`window-check.mjs` is the meaningful one: it launches the real app, attaches to
the renderer over the DevTools Protocol, and asserts the preload bridge, the
rendered sidebar, a live session, and an assistant answer in the DOM. It is the
only check that covers window → preload → IPC → ACP client → solcode → streaming
→ DOM.

## Packaging

```sh
npm run package:dir    # unpacked app in release/
npm run dist:win       # NSIS installer
npm run dist:mac       # .app
```

The packaged app expects the `solcode` binary beside its resources; ship it in
`extraResources` or set `SOLCODE_BIN`.

## Layout

```
src/
  main/
    main.ts           Electron entry: window, IPC, agent lifecycle
    acp-client.ts     ACP transport over child stdio
    agent-session.ts  Session state machine and transcript
    preload.cts       contextBridge surface (CommonJS: sandbox requires it)
  renderer/
    App.tsx           Shell: sidebar + transcript + composer
    components/       Sidebar, Transcript, ToolCard, DiffView, Composer,
                      PermissionDialog, Markdown
    theme.css         Dark-first design tokens
  shared/
    protocol.ts       Wire and renderer-facing types
```

## Not yet implemented

- Session list and resume (`session/load` is implemented in the client but no UI
  surfaces it).
- Image attachments, though solcode advertises `promptCapabilities.image`.
- A dedicated review pane; diffs render inline in tool cards today.
