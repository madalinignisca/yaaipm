#!/usr/bin/env bash
# css.sh — Build the Tailwind + DaisyUI stylesheet for ForgeDesk.
#
# Downloads the tailwindcss standalone binary and daisyUI bundle into
# bin/ (gitignored, cached across runs) and runs the compiler against
# static/css/tw-input.css, producing static/css/tw.css.
#
# No Node, no npm, no postcss — just a static binary + two .mjs plugin
# files. Matches the no-JS-build-step philosophy of the existing
# scripts/bundle.sh.
#
# Usage (must run under bash, not sh/dash — uses pipefail):
#   bash scripts/css.sh          # one-shot build (minified)
#   bash scripts/css.sh --watch  # watch mode for local dev
#   ./scripts/css.sh             # also works if executable bit is set

set -euo pipefail

BIN_DIR="bin"
mkdir -p "$BIN_DIR"

TAILWIND_BIN="$BIN_DIR/tailwindcss"
DAISY_BUNDLE="$BIN_DIR/daisyui.mjs"
DAISY_THEMES="$BIN_DIR/daisyui-theme.mjs"

# --- Pinned versions + checksums --------------------------------------------
# Do NOT track `latest` — reproducible builds require explicit versions, and
# verifying SHA-256 of downloaded artefacts mitigates supply-chain risk
# (upstream compromise, MITM during build, tag reassignment).
#
# To bump: change the version below, delete the matching checksum, run this
# script, grab the new digest from the failure message, paste it back in.
# Ship the version bump as its own PR with a changelog pointer.

TAILWIND_VERSION="v4.2.2"
DAISY_VERSION="v5.5.19"

# Per-arch binary checksums. Today only linux-x64 is verified (our VM + CI +
# production Containerfile all run linux-x64). Expand when a contributor
# needs macOS / arm64.
TAILWIND_SHA256_linux_x64="4ab84f2b496c402d3ec4fd25e0e5559fe1184d886dadae8fb4438344ec044c22"

# .mjs bundles are platform-independent.
DAISY_BUNDLE_SHA256="5fe43ff5e83c5cf519ccc32d76aa1a82ed6bfc50ee3e6f5dc6007eea6239af1f"
DAISY_THEMES_SHA256="b4071bef2e59bd83509659c1e726cc19704a1fd14ff2e56d479bc16a6a53f26d"

verify_sha256() {
  # $1 = path, $2 = expected hex digest
  local actual
  actual=$(sha256sum "$1" | awk '{print $1}')
  if [ "$actual" != "$2" ]; then
    echo "css.sh: SHA-256 mismatch for $1" >&2
    echo "  expected: $2" >&2
    echo "  actual:   $actual" >&2
    echo "  — either the pinned checksum needs updating (intentional version bump)" >&2
    echo "    or the download is corrupt / tampered. Delete bin/ and retry to" >&2
    echo "    confirm, then investigate if it repeats." >&2
    rm -f "$1"
    exit 1
  fi
}

os_arch() {
  case "$(uname -s)-$(uname -m)" in
    Linux-x86_64)   echo "linux-x64" ;;
    Linux-aarch64)  echo "linux-arm64" ;;
    Darwin-x86_64)  echo "macos-x64" ;;
    Darwin-arm64)   echo "macos-arm64" ;;
    *)              echo "unknown" ;;
  esac
}

# --- Download binaries if absent --------------------------------------------
# Absence-check keeps builds reproducible across rebuilds — to refresh,
# delete bin/ and re-run. Checksums verify every download.

if [ ! -x "$TAILWIND_BIN" ]; then
  arch=$(os_arch)
  if [ "$arch" = "unknown" ]; then
    echo "css.sh: unsupported platform $(uname -s)-$(uname -m)" >&2
    exit 1
  fi
  echo "Downloading tailwindcss $TAILWIND_VERSION ($arch)..."
  curl -sSfLo "$TAILWIND_BIN" \
    "https://github.com/tailwindlabs/tailwindcss/releases/download/$TAILWIND_VERSION/tailwindcss-$arch"
  chmod +x "$TAILWIND_BIN"
  case "$arch" in
    linux-x64) verify_sha256 "$TAILWIND_BIN" "$TAILWIND_SHA256_linux_x64" ;;
    *) echo "css.sh: no checksum pinned for arch=$arch — run locally only, not in CI" >&2 ;;
  esac
fi

if [ ! -f "$DAISY_BUNDLE" ]; then
  echo "Downloading daisyui $DAISY_VERSION bundle..."
  curl -sSfLo "$DAISY_BUNDLE" \
    "https://github.com/saadeghi/daisyui/releases/download/$DAISY_VERSION/daisyui.mjs"
  verify_sha256 "$DAISY_BUNDLE" "$DAISY_BUNDLE_SHA256"
fi

if [ ! -f "$DAISY_THEMES" ]; then
  echo "Downloading daisyui $DAISY_VERSION theme bundle..."
  curl -sSfLo "$DAISY_THEMES" \
    "https://github.com/saadeghi/daisyui/releases/download/$DAISY_VERSION/daisyui-theme.mjs"
  verify_sha256 "$DAISY_THEMES" "$DAISY_THEMES_SHA256"
fi

# --- Build -----------------------------------------------------------------

INPUT="static/css/tw-input.css"
OUTPUT="static/css/tw.css"

if [ "${1:-}" = "--watch" ]; then
  exec "$TAILWIND_BIN" -i "$INPUT" -o "$OUTPUT" --watch
else
  "$TAILWIND_BIN" -i "$INPUT" -o "$OUTPUT" --minify
  echo "Built $OUTPUT ($(wc -c < "$OUTPUT" | tr -d ' ') bytes)"
fi
