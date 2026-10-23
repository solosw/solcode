#!/usr/bin/env bash
# Build ONE release archive embedding the pinned open-computer-use runtime.
# Pure Go host build: no robotgo, CGO, GUI SDK or native cross compiler needed.
# The historical _computeruse asset suffix is retained for installer compatibility.
#
# Usage:
#   ./scripts/build-computeruse.sh <version> <goos> <goarch> <pack>
#   ./scripts/build-computeruse.sh master linux amd64 tar.gz
#   ./scripts/build-computeruse.sh master windows amd64 zip
#
# Output (appended to ./dist, unlike build-release.sh which resets it):
#   solcode_<version>_<os>_<arch>_computeruse.tar.gz | .zip

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-}"
GOOS_TARGET="${2:-}"
GOARCH_TARGET="${3:-}"
PACK="${4:-}"
if [[ -z "$VERSION" || -z "$GOOS_TARGET" || -z "$GOARCH_TARGET" || -z "$PACK" ]]; then
  echo "usage: $0 <version> <goos> <goarch> <pack>" >&2
  echo "  e.g. $0 master linux amd64 tar.gz" >&2
  exit 1
fi

case "$PACK" in
  zip|tar.gz) ;;
  *) echo "unsupported pack format: $PACK" >&2; exit 1 ;;
esac

DIST="${ROOT}/dist"
mkdir -p "$DIST"

export CGO_ENABLED=0
export GOOS="$GOOS_TARGET"
export GOARCH="$GOARCH_TARGET"
if command -v python3 >/dev/null 2>&1; then PYTHON=python3; else PYTHON=python; fi
trap 'rm -f internal/{computeruse,codegraphruntime}/bundle/assets/{runtime.archive,target.json}' EXIT
"$PYTHON" scripts/prepare-computer-use.py --os "$GOOS_TARGET" --arch "$GOARCH_TARGET"
"$PYTHON" scripts/prepare-codegraph.py --os "$GOOS_TARGET" --arch "$GOARCH_TARGET"

BIN_VERSION="${LDFLAGS_VERSION:-$VERSION}"
LDFLAGS="-s -w -X main.version=${BIN_VERSION}"

# Translate a POSIX (MSYS/Cygwin) path into a native path for helpers that are
# not path-translation aware, e.g. a native Windows python.exe running under Git
# Bash, which would read "/d/a/x" as "C:\d\a\x". No-op on Linux/macOS.
native_path() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -w "$1"
  else
    printf '%s' "$1"
  fi
}

out_name="solcode"
[[ "$GOOS_TARGET" == "windows" ]] && out_name="solcode.exe"

stage="${DIST}/stage_${GOOS_TARGET}_${GOARCH_TARGET}_computeruse"
rm -rf "$stage"
mkdir -p "$stage"
artifact="${DIST}/solcode_${VERSION}_${GOOS_TARGET}_${GOARCH_TARGET}_computeruse.${PACK}"

echo "Building solcode ${VERSION} (computer-use) for ${GOOS_TARGET}/${GOARCH_TARGET} ..."
go build -trimpath -ldflags="$LDFLAGS" -o "${stage}/${out_name}" ./cmd/solcode

(
  cp -R "${ROOT}/internal/computeruse/bundle/notices" "${stage}/open-computer-use-notices"
  cp -R "${ROOT}/internal/codegraphruntime/bundle/notices" "${stage}/codegraph-notices"
  cd "$stage"
  if [[ "$PACK" == "zip" ]]; then
    if command -v zip >/dev/null 2>&1; then
      zip -qr "${artifact}" "$out_name" open-computer-use-notices codegraph-notices
    else
      # Pass paths via argv (a native python.exe cannot resolve MSYS paths that
      # are interpolated into the script text).
      "$PYTHON" - "$(native_path "$artifact")" "$out_name" <<'PY'
import sys
from pathlib import Path
import zipfile

artifact, out_name = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(artifact, "w", zipfile.ZIP_DEFLATED) as z:
    z.write(out_name, arcname=out_name)
    for directory in ("open-computer-use-notices", "codegraph-notices"):
        for notice in Path(directory).iterdir():
            z.write(notice, arcname=str(notice))
PY
    fi
  else
    tar -czf "${artifact}" "$out_name" open-computer-use-notices codegraph-notices
  fi
)
rm -rf "$stage"

echo "Wrote ${artifact}"