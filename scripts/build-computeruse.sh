#!/usr/bin/env bash
# Build ONE computer-use (CGO + robotgo) release archive.
#
# This variant links github.com/go-vgo/robotgo, so it needs CGO enabled and a
# native toolchain for the target: MinGW-w64 (Windows), X11 dev libraries
# (Linux), or the Xcode command line tools (macOS). Cross-compiling from another
# OS is not supported — use a runner that matches <goos>/<goarch>.
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

# robotgo requires CGO; the runner must provide the native toolchain.
export CGO_ENABLED=1
export GOOS="$GOOS_TARGET"
export GOARCH="$GOARCH_TARGET"

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
go build -tags computeruse -trimpath -ldflags="$LDFLAGS" -o "${stage}/${out_name}" ./cmd/solcode

(
  cd "$stage"
  if [[ "$PACK" == "zip" ]]; then
    if command -v zip >/dev/null 2>&1; then
      zip -q "${artifact}" "$out_name"
    else
      # Pass paths via argv (a native python.exe cannot resolve MSYS paths that
      # are interpolated into the script text).
      python - "$(native_path "$artifact")" "$out_name" <<'PY'
import sys
import zipfile

artifact, out_name = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(artifact, "w", zipfile.ZIP_DEFLATED) as z:
    z.write(out_name, arcname=out_name)
PY
    fi
  else
    tar -czf "${artifact}" "$out_name"
  fi
)
rm -rf "$stage"

echo "Wrote ${artifact}"