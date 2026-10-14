# BatchTool PTC mode (experimental)

BatchTool keeps its name and switches schema when the application loads settings.

```json
{
  "batch": {
    "ptc": { "enabled": true }
  }
}
```

The default is `false`. With PTC disabled the existing `operations` batch
schema, scheduling, transaction and output handling remain unchanged.

With PTC enabled, the model supplies `{"code":"..."}` to `BatchTool`.
No Node.js installation is required: the Goja runtime is compiled into the Go
binary. Changes to this option take effect when settings are reloaded and the
tool registry is rebuilt.

```js
const found = await tools.Grep({pattern:"TODO",path:"."});
if (!found.ok) console.log(found.content);
else console.log(found.content.slice(0,1200));
```

Available names: View, Grep, Glob, LS, Edit, MultiEdit, Write, MultiWrite,
Patch, Bash and LSP. Each returns `{ok:boolean, content:string}`.
Intermediate results stay in the script; `console.log` returns selected findings.
Child calls pass through the existing Engine ToolExecutor, including its
permissions, hooks, allowlists, checkpoints, guardrails and per-tool timeouts.
PTC itself has no direct filesystem, network or process APIs.

Current limitations:
- **Child calls are synchronous**. `async/await` works but `Promise.all`
  does not give child tool concurrency yet; this is the initial PTC bridge.
- The runtime permits at most 32 child calls, a 64 KiB code payload, and a
  2-minute call deadline. Long-running child calls can exceed that deadline if
  their implementation does not observe cancellation.
- This is a same-process JavaScript VM, **not an OS security sandbox**.
  Do not treat it as safe isolation for malicious scripts. A worker-process
  resource boundary is needed before claiming hard memory isolation.
- Console output is bounded, not a durable ResultStore; long logs should be
  filtered by the script. Atomic multi-file edits remain available in the
  legacy engine, but are not an implicit property of PTC scripts.
- If the script fails, there is no automatic fallback to the legacy schema.
