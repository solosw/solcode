# Embedded CodeGraph

SolCode release builds embed **sunerpy/codegraph-rust v0.55.2** with the pinned
`solcode-external-index-v1` storage patch, a single native
executable with bundled SQLite, tree-sitter grammars and a precompiled browser
viewer. No Node/npm/Python runtime, external MCP configuration or Git hooks are
required. The upstream project is MIT-licensed; dependency notices ship alongside
the release and are also embedded.

## Lifecycle and storage

- Opening a project schedules initialization in the background after successful
  application construction; chatting does not wait for indexing.
- SolCode invokes the pinned native CLI as `init <root> --target none --yes`. For
  this version, these options initialize only the index, without installing
  hooks, Agent files or MCP configuration. Home/filesystem roots, symlinked index
  directories and unrecognized raw databases are rejected.
- Native indexes live in **`~/.solcode/projects/<project-id>/codegraph/`**. The
  project identifier follows the existing SolCode project-state convention,
  derived from the canonical absolute project path. Project aliases share one
  index and daemon. SolCode passes the absolute path through `CODEGRAPH_DIR`.
  The patch accepts external storage and anchors lease creation there; upstream
  still owns scanning, SQLite, daemon and watcher behavior. External indexes
  with a different physical project owner are refused, not adopted or rebuilt.
- Previous `.solcode-codegraph/` and Node-engine `.codegraph/` indexes are left
  untouched: no migration, deletion or stopping their existing daemons. A new
  index is built in user state when the updated application opens the project.
  No new project-local index or root ignore entry is needed. This is not the
  changegraph edit-history database.
- Upstream owns scan leases, index-state records and interrupted-scan recovery.
  SolCode does not implement another scanner, graph store or watcher. Unknown,
  future-format or corrupt indexes are not silently replaced.
- MCP sessions share the upstream daemon, which owns file watching and
  incremental reconciliation. Closing/reloading one session releases only its
  proxy, not other sessions or their daemon. Idle shutdown is upstream-managed.
- Before reporting ready, a background status query pinned internally to this
  project asks the upstream service broker to retain a shared-daemon lease and
  complete one catch-up. This also covers concurrent warm-start read-only
  fallbacks; model-supplied project overrides remain forbidden. Later watcher
  updates are **asynchronous**. Verify source before editing.
- Telemetry is disabled. Runtime resolution never downloads anything and has no
  PATH fallback. Missing payloads, unsupported platforms or startup failures
  leave Grep/View/LSP and the existing projectindex fallback usable. The existing
  changegraph edit history is retained.

## Agent queries

The core `CodeGraph` tool exposes only `explore`, `node`, `search`, `callers`,
`callees`, `impact`, `files` and `status`. It rejects SQL, management commands,
extra parameters and project overrides. File/path arguments must remain
project-relative. During initialization, use ordinary tools rather than polling.

```json
{"action":"explore","query":"AuthService login session","maxFiles":6}
{"action":"node","symbol":"Login","file":"internal/auth/service.go","includeCode":true}
{"action":"impact","symbol":"Login","depth":2}
```

Disable explicitly in settings:

```json
{"codegraph":{"enabled":false}}
```

## Source and release builds

A plain `go build` of a clean source checkout has no large runtime payload. Use
`bash scripts/build-codegraph.sh -o /tmp/solcode` to prepare the current Go
target's patched native archive and embed it. **Build-time requirements:**
Python 3.8+, Git, Rust/Cargo 1.98+ and a C toolchain (Linux musl targets also need
`musl-tools`; Windows targets use MSVC). CI pins Rust 1.98.0. The builder fetches
the exact source commit, checks/applies the SHA256-pinned patch, runs path/lease/
rebuild tests, and compiles with Cargo `--locked`. End users need none of these
toolchains. On Windows hosts:

```powershell
python scripts/prepare-codegraph.py --os windows --arch amd64
go build ./cmd/solcode
```

`build-release.sh`, `build-release.ps1` and the historical `build-computeruse.sh`
assets used by master publishing prepare both runtimes for each target, include
notices and clean generated payloads on exit. The manual multi-platform scripts
require Rust target standard libraries and native/cross C linkers for **every**
target; Go cross-compilation alone is no longer sufficient. Prefer one native
`build-computeruse.sh <version> <os> <arch> <pack>` build per host, as the release
workflows do. Semver releases retain both historical archive names for each
native build. Do not build different targets concurrently in one checkout:
each replaces its target-specific embed payload.

`release.json` pins the source commit and patch digest; its asset digests record
the original upstream archives, not the patched build output. The builder
records source/patch provenance and the actual archive SHA256 in `target.json`,
which is embedded with the payload. Runtime resolution checks all three,
rejecting stale official/unpatched payloads. This trusts the local build
environment and cache, not an upstream signature for the modified binary.
Build caches use `SOLCODE_CODEGRAPH_BUILD_CACHE` or
`~/.cache/solcode-codegraph-build/`; these are build artifacts, not project indexes.

At runtime, the verified archive is extracted atomically to
`~/.solcode/codegraph-runtime/<version>-<patch-revision>/<platform>/<payload-and-notice-hash>/`.
Extraction rejects escaping paths/links and enforces size/entry limits. There is
no fallback to a system CodeGraph. For development only, `codegraph.runtime_dir`
may point to a directory containing the unpacked `codegraph` (`codegraph.exe` on
Windows). This explicit override is trusted like a configured MCP command and
is not checksum-verified; never point it at an untrusted directory.

## Historical size comparison (macOS arm64, unpatched upstream archive)

These pre-patch measurements are retained for context, **not as size claims for
the current patched-source build**. They embed CodeGraph only, without the optional ComputerUse
runtime. The default `go build` comparison uses the same settings for both
engines (CGO enabled, debug information retained); the tarballs contain only the
SolCode executable.

| Artifact | Previous Node engine | Native engine |
| --- | ---: | ---: |
| Default development executable | 201.87 MiB | 160.91 MiB |
| Development executable `.tar.gz` | 122.35 MiB | 79.85 MiB |

The native upstream archive itself is **11.71 MiB** (59.82 MiB extracted).
A separate release-style check with `CGO_ENABLED=0`, `-trimpath` and
`-ldflags="-s -w"` produces a **129.00 MiB** SolCode executable and a
**55.13 MiB** tarball including both sets of notices. This is not a size claim
for a full release that also embeds ComputerUse, or for other platforms.

## Verification and upgrades

```sh
python3 -m unittest discover -s scripts -p test_prepare_codegraph.py -v
go test -race ./internal/codegraphruntime/... ./internal/mcp ./internal/tool ./internal/config ./internal/app -run 'TestCodeGraph|TestExtract|TestRuntimeOverride|TestSourceBuild|TestPinnedRelease|TestPatchedPayload|TestPreparedPayload'
python3 scripts/prepare-codegraph.py --os darwin --arch arm64
SOLCODE_CODEGRAPH_SMOKE=1 go test -race ./internal/mcp -run '^TestCodeGraph.*Smoke$' -v -count=1
```

The opt-in tests exercise real cold initialization, all eight queries,
simultaneous sessions sharing one daemon, live edits, file deletion, offline
catch-up, interrupted-scan recovery, legacy-index preservation, and rejection of
foreign databases, different project owners and symlinked indexes. They check that hooks, root ignore rules
and Agent/MCP configuration are not installed. Only temporary projects are used.

To upgrade, review the native CLI/MCP contracts and installer side effects,
update `bundle/release.json` with all six official digests and the source commit,
review/rebase `scripts/codegraph/external-index.patch`, update its SHA256 and
revision in the release lock, and regenerate checked-in notices with **Python 3.11+**:

```sh
python3 scripts/collect-codegraph-notices.py --source <pinned-checkout>
```

The collector verifies crate archives against Cargo.lock and viewer packages
against package-lock integrity records, retaining a cross-platform dependency
superset (including build dependencies). Some workspace helpers omit license
files; their locked parent packages supply the workspace licenses. Two npm
packages publish only an MIT declaration: their original package metadata,
attribution and README are retained along with the standard MIT permission text.
Normal release builds use the checked-in notices and do not run this collector.
Run the real smoke tests on each supported OS/architecture before publishing.
