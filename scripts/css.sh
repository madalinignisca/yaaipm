#!/usr/bin/env bash
# css.sh — Build the Tailwind + DaisyUI stylesheet for the debate spike.
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

# --- Download binaries if absent --------------------------------------------
# We don't auto-update — pinning by absence-check keeps builds reproducible
# across rebuilds. To refresh, delete bin/ and re-run.

os_arch() {
  case "$(uname -s)-$(uname -m)" in
    Linux-x86_64)   echo "linux-x64" ;;
    Linux-aarch64)  echo "linux-arm64" ;;
    Darwin-x86_64)  echo "macos-x64" ;;
    Darwin-arm64)   echo "macos-arm64" ;;
    *)              echo "unknown" ;;
  esac
}

if [ ! -x "$TAILWIND_BIN" ]; then
  arch=$(os_arch)
  if [ "$arch" = "unknown" ]; then
    echo "css.sh: unsupported platform $(uname -s)-$(uname -m)" >&2
    exit 1
  fi
  echo "Downloading tailwindcss-$arch..."
  curl -sSfLo "$TAILWIND_BIN" \
    "https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-$arch"
  chmod +x "$TAILWIND_BIN"
fi

if [ ! -f "$DAISY_BUNDLE" ]; then
  echo "Downloading daisyui.mjs..."
  curl -sSfLo "$DAISY_BUNDLE" \
    "https://github.com/saadeghi/daisyui/releases/latest/download/daisyui.mjs"
fi

if [ ! -f "$DAISY_THEMES" ]; then
  echo "Downloading daisyui-theme.mjs..."
  curl -sSfLo "$DAISY_THEMES" \
    "https://github.com/saadeghi/daisyui/releases/latest/download/daisyui-theme.mjs"
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
