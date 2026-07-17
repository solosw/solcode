#!/usr/bin/env bash
# Build multi-platform release archives for GitHub Releases.
#
# Usage:
#   ./scripts/build-release.sh v0.1.0
#   VERSION=v0.1.0 ./scripts/build-release.sh
#
# Output under ./dist:
#   solcode_<version>_<os>_<arch>.tar.gz | .zip
#   checksums.txt

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-${VERSION:-}}"
if [[ -z "$VERSION" ]]; then
  echo "usage: $0 <version>   e.g. v0.1.0" >&2
  exit 1
fi

DIST="${ROOT}/dist"
rm -rf "$DIST"
mkdir -p "$DIST"

export CGO_ENABLED=0
# Binary version for -version (can differ from asset tag, e.g. master+abc1234).
BIN_VERSION="${LDFLAGS_VERSION:-$VERSION}"
LDFLAGS="-s -w -X main.version=${BIN_VERSION}"

targets=(
  "linux amd64 tar.gz"
  "linux arm64 tar.gz"
  "darwin amd64 tar.gz"
  "darwin arm64 tar.gz"
  "windows amd64 zip"
  "windows arm64 zip"
)

echo "Building solcode ${VERSION} ..."
for spec in "${targets[@]}"; do
  # shellcheck disable=SC2086
  set -- $spec
  goos="$1"
  goarch="$2"
  pack="$3"

  out_name="solcode"
  [[ "$goos" == "windows" ]] && out_name="solcode.exe"

  stage="${DIST}/stage_${goos}_${goarch}"
  mkdir -p "$stage"
  artifact="${DIST}/solcode_${VERSION}_${goos}_${goarch}.${pack}"

  echo "  -> ${goos}/${goarch}"
  GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="$LDFLAGS" -o "${stage}/${out_name}" ./cmd/solcode

  (
    cd "$stage"
    if [[ "$pack" == "zip" ]]; then
      if command -v zip >/dev/null 2>&1; then
        zip -q "${artifact}" "$out_name"
      else
        # Fallback: PowerShell on Windows Git Bash, or python
        if command -v powershell.exe >/dev/null 2>&1; then
          powershell.exe -NoProfile -Command "Compress-Archive -Path '$out_name' -DestinationPath '$(cygpath -w "$artifact" 2>/dev/null || echo "$artifact")' -Force"
        else
          python - <<PY
import zipfile
z=zipfile.ZipFile(r'''${artifact}''','w',zipfile.ZIP_DEFLATED)
z.write(r'''${out_name}''', arcname=r'''${out_name}''')
z.close()
PY
        fi
      fi
    else
      tar -czf "${artifact}" "$out_name"
    fi
  )
  rm -rf "$stage"
done

(
  cd "$DIST"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum solcode_${VERSION}_* > checksums.txt
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 solcode_${VERSION}_* > checksums.txt
  else
    echo "warning: no sha256 tool; skipping checksums.txt" >&2
  fi
)

echo
echo "Artifacts in ${DIST}:"
ls -la "$DIST"
echo
echo "Upload these files to a GitHub Release tagged ${VERSION}."
