#!/usr/bin/env bash
# Install solcode from the rolling "master" GitHub Release (no Go required).
#
# CI publishes binaries on every push to master under release tag "master".
# There is no "latest" version channel — install always tracks master unless
# you pin --version to a specific release tag (e.g. v0.1.0).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/solosw/solcode/master/scripts/install.sh | bash
#   curl -fsSL ... | bash -s -- --dir ~/bin
#   curl -fsSL ... | bash -s -- --version v0.1.0   # optional pinned tag
#   SOLCODE_REPO=myorg/solcode ./scripts/install.sh
#
# Env:
#   SOLCODE_REPO         GitHub owner/repo (default: solosw/solcode)
#   SOLCODE_VERSION      Release tag (default: master)
#   SOLCODE_INSTALL_DIR  Install directory (default: ~/.local/bin)
#   GITHUB_TOKEN         Optional; raises API rate limit / private repos

set -euo pipefail

REPO="${SOLCODE_REPO:-solosw/solcode}"
VERSION="${SOLCODE_VERSION:-master}"
INSTALL_DIR="${SOLCODE_INSTALL_DIR:-${HOME}/.local/bin}"
BINARY_NAME="solcode"
GITHUB_BASE="${GITHUB_BASE:-https://github.com}"
NO_PATH=0

usage() {
  cat <<'EOF'
Install solcode binary from GitHub (default: master branch build).

Options:
  --version TAG   Release tag (default: master, or $SOLCODE_VERSION)
  --dir PATH      Install directory (default: ~/.local/bin)
  --repo OWNER/NAME
  --no-path       Do not modify shell config / PATH
  --help

Notes:
  - Default channel is "master" (rolling build from the master branch).
  - There is no "latest" channel; "latest" is treated as "master".
  - PATH is updated automatically for the current shell and common rc files.
  - Pin a semver tag with --version vX.Y.Z if you publish versioned releases.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version|-v)
      VERSION="${2:?missing version}"
      shift 2
      ;;
    --dir|-d)
      INSTALL_DIR="${2:?missing dir}"
      shift 2
      ;;
    --repo)
      REPO="${2:?missing repo}"
      shift 2
      ;;
    --no-path)
      NO_PATH=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

# No "latest" channel — map to master.
if [[ "$VERSION" == "latest" || -z "$VERSION" ]]; then
  VERSION="master"
fi

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: required command not found: $1" >&2
    exit 1
  }
}

need_cmd uname
need_cmd mktemp
need_cmd mkdir
need_cmd chmod
need_cmd mv
need_cmd rm

if command -v curl >/dev/null 2>&1; then
  DOWNLOAD_CMD="curl"
elif command -v wget >/dev/null 2>&1; then
  DOWNLOAD_CMD="wget"
else
  echo "error: need curl or wget" >&2
  exit 1
fi

http_get() {
  local url="$1"
  local out="${2:-}"
  if [[ "$DOWNLOAD_CMD" == "curl" ]]; then
    local args=(-fsSL)
    if [[ -n "${GITHUB_TOKEN:-}" ]]; then
      args+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
    fi
    if [[ -n "$out" ]]; then
      curl "${args[@]}" -o "$out" "$url"
    else
      curl "${args[@]}" "$url"
    fi
  else
    if [[ -n "${GITHUB_TOKEN:-}" ]]; then
      if [[ -n "$out" ]]; then
        wget -q --header="Authorization: Bearer ${GITHUB_TOKEN}" -O "$out" "$url"
      else
        wget -q --header="Authorization: Bearer ${GITHUB_TOKEN}" -O - "$url"
      fi
    else
      if [[ -n "$out" ]]; then
        wget -q -O "$out" "$url"
      else
        wget -q -O - "$url"
      fi
    fi
  fi
}

detect_os() {
  local os
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    linux*) echo "linux" ;;
    darwin*) echo "darwin" ;;
    msys*|mingw*|cygwin*) echo "windows" ;;
    *)
      echo "error: unsupported OS: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    armv7l|armv7) echo "arm" ;;
    i386|i686) echo "386" ;;
    *)
      echo "error: unsupported architecture: $arch" >&2
      exit 1
      ;;
  esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"
TAG="$VERSION"
ASSET_VERSION="$TAG"
EXT="tar.gz"
ARCHIVE_BIN="$BINARY_NAME"
if [[ "$OS" == "windows" ]]; then
  EXT="zip"
  ARCHIVE_BIN="${BINARY_NAME}.exe"
fi

ASSET="solcode_${ASSET_VERSION}_${OS}_${ARCH}.${EXT}"
ALT_ASSET="solcode_${ASSET_VERSION#v}_${OS}_${ARCH}.${EXT}"

TMPDIR="$(mktemp -d)"
cleanup() { rm -rf "$TMPDIR"; }
trap cleanup EXIT

download_asset() {
  local name="$1"
  local url="${GITHUB_BASE}/${REPO}/releases/download/${TAG}/${name}"
  local dest="${TMPDIR}/${name}"
  echo "Downloading ${name} ..." >&2
  if http_get "$url" "$dest"; then
    printf '%s\n' "$dest"
    return 0
  fi
  return 1
}

echo "Channel/tag: ${TAG}"
echo "Repo:        ${REPO}"
echo "Target:      ${OS}/${ARCH}"

ARCHIVE=""
if ARCHIVE="$(download_asset "$ASSET")"; then
  :
elif [[ "$ALT_ASSET" != "$ASSET" ]] && ARCHIVE="$(download_asset "$ALT_ASSET")"; then
  :
else
  echo "error: failed to download release asset for ${OS}/${ARCH}" >&2
  echo "tried: ${ASSET}" >&2
  [[ "$ALT_ASSET" != "$ASSET" ]] && echo "       ${ALT_ASSET}" >&2
  echo "tag:   ${TAG}" >&2
  echo "repo:  ${REPO}" >&2
  echo >&2
  echo "hint: push to master so CI publishes the rolling \"master\" release," >&2
  echo "      or pass --version <tag> for a versioned release." >&2
  exit 1
fi

echo "Extracting ..."
case "$EXT" in
  tar.gz)
    need_cmd tar
    tar -xzf "$ARCHIVE" -C "$TMPDIR"
    ;;
  zip)
    if command -v unzip >/dev/null 2>&1; then
      unzip -qo "$ARCHIVE" -d "$TMPDIR"
    else
      echo "error: unzip is required for Windows zip assets" >&2
      exit 1
    fi
    ;;
esac

SRC=""
if [[ -f "${TMPDIR}/${ARCHIVE_BIN}" ]]; then
  SRC="${TMPDIR}/${ARCHIVE_BIN}"
else
  SRC="$(find "$TMPDIR" -type f \( -name "$ARCHIVE_BIN" -o -name "$BINARY_NAME" \) 2>/dev/null | head -n1 || true)"
fi
if [[ -z "$SRC" || ! -f "$SRC" ]]; then
  echo "error: binary not found inside archive" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
DEST="${INSTALL_DIR}/${ARCHIVE_BIN}"
if [[ "$OS" != "windows" ]]; then
  DEST="${INSTALL_DIR}/${BINARY_NAME}"
fi

mv -f "$SRC" "$DEST"
chmod +x "$DEST"

echo
echo "Installed: ${DEST}"
echo "Version:   ${TAG}"

# --- PATH automation ---
# Managed block markers (idempotent re-installs).
PATH_MARKER_BEGIN="# >>> solcode PATH >>>"
PATH_MARKER_END="# <<< solcode PATH <<<"
PATH_LINE="export PATH=\"${INSTALL_DIR}:\$PATH\""

path_already_in_env() {
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) return 0 ;;
    *) return 1 ;;
  esac
}

append_path_block() {
  local file="$1"
  local dir
  dir="$(dirname "$file")"
  mkdir -p "$dir" 2>/dev/null || true
  touch "$file" 2>/dev/null || return 1

  if grep -Fqs "$PATH_MARKER_BEGIN" "$file" 2>/dev/null; then
    # Refresh block in place (portable: rewrite temp file).
    local tmp
    tmp="$(mktemp)"
    awk -v begin="$PATH_MARKER_BEGIN" -v end="$PATH_MARKER_END" -v line="$PATH_LINE" '
      $0 == begin { print; print line; skip=1; next }
      $0 == end { skip=0; print; next }
      skip { next }
      { print }
    ' "$file" >"$tmp" && mv "$tmp" "$file"
    return 0
  fi

  {
    printf '\n%s\n%s\n%s\n' "$PATH_MARKER_BEGIN" "$PATH_LINE" "$PATH_MARKER_END"
  } >>"$file"
}

ensure_path() {
  if [[ "$NO_PATH" -eq 1 ]]; then
    echo "Skipped PATH update (--no-path)."
    return 0
  fi

  # Current shell (for this process tree; also useful if user sources the script).
  if ! path_already_in_env; then
    export PATH="${INSTALL_DIR}:${PATH}"
    echo "Updated PATH for current session: ${INSTALL_DIR}"
  else
    echo "PATH already includes ${INSTALL_DIR} (current session)"
  fi

  # Persist into common shell configs (only files that exist or are default for $SHELL).
  local shell_name rc_candidates=() updated=()
  shell_name="$(basename "${SHELL:-}")"

  case "$shell_name" in
    zsh)
      rc_candidates+=("${ZDOTDIR:-$HOME}/.zshrc")
      ;;
    bash)
      rc_candidates+=("$HOME/.bashrc")
      # macOS login shells often only read .bash_profile
      rc_candidates+=("$HOME/.bash_profile")
      ;;
    fish)
      rc_candidates+=("$HOME/.config/fish/config.fish")
      ;;
    *)
      rc_candidates+=("$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.profile")
      ;;
  esac

  # Always ensure at least one common file is updated so new terminals work.
  # Prefer existing files; if none exist, create the primary for this shell.
  local existing=()
  local f
  for f in "${rc_candidates[@]}"; do
    [[ -f "$f" ]] && existing+=("$f")
  done

  if [[ ${#existing[@]} -eq 0 ]]; then
    case "$shell_name" in
      zsh) existing+=("${ZDOTDIR:-$HOME}/.zshrc") ;;
      fish) existing+=("$HOME/.config/fish/config.fish") ;;
      *) existing+=("$HOME/.bashrc") ;;
    esac
  fi

  for f in "${existing[@]}"; do
    if [[ "$shell_name" == "fish" && "$f" == *config.fish ]]; then
      # fish uses a different syntax
      local fish_begin="# >>> solcode PATH >>>"
      local fish_end="# <<< solcode PATH <<<"
      local fish_line="fish_add_path -m ${INSTALL_DIR}"
      mkdir -p "$(dirname "$f")" 2>/dev/null || true
      touch "$f" 2>/dev/null || continue
      if grep -Fqs "$fish_begin" "$f" 2>/dev/null; then
        local tmp
        tmp="$(mktemp)"
        awk -v begin="$fish_begin" -v end="$fish_end" -v line="$fish_line" '
          $0 == begin { print; print line; skip=1; next }
          $0 == end { skip=0; print; next }
          skip { next }
          { print }
        ' "$f" >"$tmp" && mv "$tmp" "$f"
      else
        printf '\n%s\n%s\n%s\n' "$fish_begin" "$fish_line" "$fish_end" >>"$f"
      fi
      updated+=("$f")
      continue
    fi

    if append_path_block "$f"; then
      updated+=("$f")
    fi
  done

  if [[ ${#updated[@]} -gt 0 ]]; then
    echo "Persisted PATH in:"
    for f in "${updated[@]}"; do
      echo "  $f"
    done
    echo "New terminals will pick this up automatically."
    # If user ran via curl|bash, their interactive shell won't see export;
    # give a one-liner they can eval, but PATH files are already done.
    if [[ "${BASH_SOURCE[0]:-}" == "" || "${BASH_SOURCE[0]:-}" == "bash" || "${BASH_SOURCE[0]:-}" == "/dev/stdin" || "${BASH_SOURCE[0]:-}" == "stdin" ]]; then
      echo
      echo "For this already-open shell, run:"
      echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
      echo "  # or: hash -r; hash -p ${DEST} ${BINARY_NAME} 2>/dev/null || true"
    fi
  fi
}

ensure_path

echo
echo "Next:"
echo "  export ANTHROPIC_API_KEY=sk-ant-..."
echo "  ${BINARY_NAME}"
echo
echo "Done."

