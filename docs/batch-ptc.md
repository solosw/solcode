# BatchTool PTC mode (experimental)

BatchTool keeps its name and switches schema when the application loads settings.

```json
{
  "batch": {
    "ptc": { "enabled": true },
    "max_parallel": 4,
    "max_result_runes": 18000
  }
}
```

The default is `false`. With PTC disabled the existing `operations` batch
schema, scheduling, transaction and output handling remain unchanged.

With PTC enabled, the model supplies `{"code":"..."}` to `BatchTool`.
No Node.js installation is required: the Goja runtime is compiled into the Go
binary. Changes to this option take effect when settings are reloaded and the
tool registry is rebuilt. PTC requests reject unknown envelope fields; legacy
`operations`, `depends_on`, `output_mode` and `atomic` are not PTC options.
Child argument schemas are included in the PTC tool description and validated
before dispatch.

## Asynchronous calls

```js
const matches = await Promise.all([
  tools.Grep({pattern: "TODO", path: "internal"}),
  tools.Glob({pattern: "**/*_test.go", path: "internal"})
]);
for (const match of matches) {
  if (!match.ok) console.log(match.error || match.content);
  else console.log(match.content.slice(0, 1200));
}
```

Available names: View, Grep, Glob, LS, Edit, MultiEdit, Write, MultiWrite,
Patch, Bash and LSP. Each returns a Promise. Always await dependent calls;
`Promise.all` actually dispatches independent safe reads concurrently, bounded
by `batch.max_parallel`. Only tools declaring both read-only and concurrency-safe
are eligible. Mutations, Bash, and unsafe reads act as serial barriers; no read
or other write overlaps them. Calls are queued in JavaScript invocation order,
run in bounded waves, and settled in that order on a single VM goroutine.
Do not rely on `Promise.race` to cancel tool calls or return before a wave finishes.

Intermediate results stay in the script; `console.log` returns selected findings.
Child calls pass through the existing Engine ToolExecutor, including its
permissions, hooks, allowlists, checkpoints, guardrails and per-tool timeouts.
Allowing BatchTool alone never authorizes its children. Arbitrary PTC code is
conservatively classified as non-read-only, so plan mode denies the PTC container
rather than attempting to prove JavaScript is read-only.

## ResultStore references

Text results include:

```js
// {ok, content, result_ref, truncated, offset, next_offset, total_runes}
const result = await tools.View({path: "large.txt"});
if (!result.ok) {
  console.log(result.error || result.content);
} else {
  console.log(result.content); // bounded preview
  for (let offset = result.next_offset; offset < result.total_runes;) {
    const page = results.get(result.result_ref, {offset, limit: 1000});
    if (page.error) throw new Error(page.error);
    // Filter/process the page here instead of printing the entire result.
    console.log(page.content.match(/important.*/g) || []);
    offset = page.next_offset;
  }
}
```

- Full text is retained in an **invocation-local in-memory** ResultStore, not
  re-sent to the model. This is not a durable cross-invocation ResultStore.
- `content` and each `results.get` page are bounded by `batch.max_result_runes`.
  Offsets and lengths count Unicode code points, not bytes or JavaScript UTF-16
  code units. Use `next_offset`, not JavaScript `content.length`, to paginate.
- `results.get(ref, {offset, limit})` is synchronous and does not count as a
  child tool call. Default offset is 0; omitted/zero limit uses the preview
  bound. Larger limits are clamped. Invalid references return
  `{ok:false, content:"", error:"RESULT_NOT_FOUND"}`; invalid ranges are rejected.
- References only identify entries from the current script. They do not name
  filesystem paths, other sessions, or previous invocations.
- The store retains at most **8 MiB** of text. If the next result cannot fit,
  its bounded preview and original `ok` status are returned with `storage_error`
  and **without** `result_ref`; no full-result retention is claimed. A successful
  write stays successful even when its receipt cannot be fully stored.
- Bridge failures use `{ok:false, content:"", error}`. Ordinary child failures
  preserve `ok:false` and the tool's error text in `content`; these text results
  can also be paginated. Image payloads are not supported.

## Limits and failure semantics

- At most **32 child call attempts**, a **64 KiB** code payload, and a
  **2-minute** invocation deadline. This call-attempt bound is independent of
  legacy `batch.max_operations`.
- Console output is accumulated incrementally and bounded to **16384 Unicode
  code points**, with a truncation marker. Errors are bounded too.
- Script loops and JavaScript continuations are interrupted on cancellation.
  Queued calls are not dispatched after cancellation. Already-running children
  must observe their context; non-cooperative Go code can exceed the deadline.
- Script failure is reported with no automatic retry, schema fallback, or
  rollback. Completed tool effects remain applied; queued calls are discarded
  when the main script rejects. Use explicit child MultiEdit/MultiWrite atomic
  options for transactional mutations. Independent calls within a started wave
  may already have completed when an error occurs.
- PTC has no direct filesystem, network, process, timers, `require` or Node APIs.
  It cannot recursively invoke BatchTool. Each invocation gets a fresh VM and
  ResultStore. An unresolved promise or unhandled rejection (including detached
  async continuations) is an error, not a successful completion.
- This is a same-process JavaScript VM, **not an OS security sandbox**. The text
  and call limits do not bound arbitrary JavaScript heap allocations. Do not
  treat it as safe isolation for malicious scripts. A worker-process resource
  boundary is still needed before claiming hard memory isolation.

## Verification

```sh
go mod verify
go build ./...
go test ./internal/tool ./internal/engine -run 'TestBatch|TestPTC' -count=1
go test -race ./internal/tool ./internal/engine -run 'TestBatch|TestPTC' -count=1
```

Tests cover concurrent reads and their bound, mutation barriers, dependent
calls, strict validation, call/output/store limits, Unicode paging, isolated
references/VM globals, cancellation, script errors, executor allowlists,
permissions, pre-tool hooks, guardrails, and checkpoint preservation.

## ACP startup troubleshooting

PTC does not start Node.js or a JavaScript VM during the ACP handshake. The
embedded JavaScript runtime is created only when a PTC `BatchTool` call runs.
If the ACP process exits before `initialize`, inspect its **stderr** first.

Settings must be valid JSON, including commas when adding the `batch` block
beside existing fields. A malformed `~/.solcode/settings.local.json` stops
configuration loading before ACP starts; it is not a PTC execution failure.
Configuration parse errors include the file path, line and column without
printing potentially secret config values. Fix the JSON instead of silently
falling back to defaults (which could change providers or permissions).

Runtime settings saves publish a complete temporary file using a
same-directory rename, so a simultaneous startup cannot read a half-written
in-place update on filesystems that support atomic replacement. This does not
serialize concurrent read/modify/write updates from multiple processes, or
make external editors' in-place writes atomic.
