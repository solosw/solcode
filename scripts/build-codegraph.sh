#!/usr/bin/env bash
# Build with patched CodeGraph embedded (Python, Git, Rust >=1.98, C toolchain).
# Non-native GOOS/GOARCH also need the Rust target stdlib and C linker.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
if command -v python3 >/dev/null 2>&1; then PYTHON=python3; else PYTHON=python; fi
"$PYTHON" scripts/prepare-codegraph.py --os "`go env GOOS`" --arch "`go env GOARCH`"
go build "$@" ./cmd/solcode
