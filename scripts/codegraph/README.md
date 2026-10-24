# CodeGraph external-index patch

Base: `sunerpy/codegraph-rust` v0.55.2, commit
`e62ccab464ea558f41e88077383b38689605bb14` (MIT). SolCode distributes
this small source modification under the same MIT terms; upstream attribution
and dependency licenses are retained in the bundled notices.

`external-index.patch` is checked against `bundle/release.json` before applying:

- Accept an absolute `CODEGRAPH_DIR`, retaining relative-name validation,
  physical project identity, unsafe-alias and symlink/reparse-point checks.
- Anchor capability-relative lease creation at the validated index parent,
  rather than assuming the index is a direct child of the source project.
- Retain the upstream owner gate under the exclusive rebuild lease: external
  indexes cannot be adopted by another physical project during `init`. The
  upstream copied-project behavior remains unchanged for project-local indexes.

Scanning, SQLite, state protocol, service sharing, daemon and watcher remain
upstream implementations. No dependencies are added. Path, lease and rebuild
tests run during preparation. See `docs/codegraph-runtime.md` for build and
real-runtime smoke commands.

When changing/rebasing the patch, update its SHA256 and revision in the release
lock, rebuild all platform payloads, and run the smoke suite on each platform.
Old project-local indexes are deliberately neither migrated nor removed.
