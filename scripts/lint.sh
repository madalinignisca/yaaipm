#!/usr/bin/env bash
# lint.sh — Run golangci-lint at the exact version CI uses.
#
# Why this exists: the workflow used to pin `version: v2.11`, a FLOATING
# minor that resolves to whatever the latest v2.11.x is on the day. CI
# silently moved 2.11.1 -> 2.11.4, the dev VM did not, and the two
# disagreed about real findings (goconst, govet shadow, misspell). A green
# local lint stopped predicting a green CI lint, which cost several
# push/wait/fix cycles (issue #120).
#
# The version now lives in .golangci-version, read by BOTH this script and
# .github/workflows/lint.yml, so local and CI cannot drift apart without
# someone editing that file deliberately.
#
# Mirrors scripts/css.sh: pinned version, SHA-256 verified, cached in bin/
# (gitignored), no package manager required.
#
# Usage (must run under bash — uses pipefail):
#   bash scripts/lint.sh              # lint ./...
#   bash scripts/lint.sh ./internal/... --fix
#
# To bump: change .golangci-version, add the new digest to CHECKSUMS below
# (get it from the release's golangci-lint-<ver>-checksums.txt), and ship
# the bump as its own PR so the diff shows exactly what changed.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

VERSION_FILE=".golangci-version"
[ -f "$VERSION_FILE" ] || { echo "missing $VERSION_FILE" >&2; exit 1; }
VERSION="$(tr -d '[:space:]' < "$VERSION_FILE")"

# Per-version SHA-256 of the linux-amd64 tarball, from the release's own
# checksums file. Hardcoded rather than fetched: fetching the digest from
# the same host that serves the artefact verifies transport, not
# authenticity, so it would not detect a compromised or reassigned tag.
#
# Only linux-amd64 is pinned today — the dev VM, CI, and the production
# Containerfile all run linux-amd64. Add entries when someone needs
# darwin/arm64.
declare -A CHECKSUMS=(
  ["v2.11.4"]="200c5b7503f67b59a6743ccf32133026c174e272b930ee79aa2aa6f37aca7ef1"
)

EXPECTED_SHA="${CHECKSUMS[$VERSION]:-}"
if [ -z "$EXPECTED_SHA" ]; then
  cat >&2 <<EOF
No pinned checksum for golangci-lint $VERSION.

Add one to CHECKSUMS in scripts/lint.sh:
  curl -sSfL https://github.com/golangci/golangci-lint/releases/download/$VERSION/golangci-lint-${VERSION#v}-checksums.txt \\
    | grep linux-amd64.tar.gz
EOF
  exit 1
fi

# golangci-lint shells out to `go` for package loading. Without it the
# failure is four lines of "go env" noise ending in exit 3, which reads
# like a lint failure rather than a missing toolchain. Fail early and say
# so. /usr/local/go/bin is the standard tarball install location and is
# not on a non-interactive shell's default PATH on this VM.
if ! command -v go >/dev/null 2>&1; then
  if [ -x /usr/local/go/bin/go ]; then
    export PATH="/usr/local/go/bin:$PATH"
  else
    echo "go not found on PATH — golangci-lint needs it to load packages." >&2
    echo "Install Go, or add its bin directory to PATH (e.g. /usr/local/go/bin)." >&2
    exit 1
  fi
fi

BIN_DIR="bin"
BIN="$BIN_DIR/golangci-lint"
STAMP="$BIN_DIR/.golangci-lint.version"
mkdir -p "$BIN_DIR"

# Re-download when the binary is missing OR was built from a different
# version — otherwise a version bump would silently keep linting with the
# stale cached binary, which is the exact failure this script prevents.
if [ ! -x "$BIN" ] || [ "$(cat "$STAMP" 2>/dev/null || true)" != "$VERSION" ]; then
  TARBALL="$BIN_DIR/golangci-lint-${VERSION}.tar.gz"
  URL="https://github.com/golangci/golangci-lint/releases/download/${VERSION}/golangci-lint-${VERSION#v}-linux-amd64.tar.gz"
  echo "Downloading golangci-lint $VERSION..."
  curl -sSfLo "$TARBALL" "$URL"

  ACTUAL_SHA="$(sha256sum "$TARBALL" | awk '{print $1}')"
  if [ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]; then
    rm -f "$TARBALL"
    echo "SHA-256 mismatch for golangci-lint $VERSION" >&2
    echo "  expected: $EXPECTED_SHA" >&2
    echo "  actual:   $ACTUAL_SHA" >&2
    exit 1
  fi

  tar -xzf "$TARBALL" -C "$BIN_DIR" --strip-components=1 \
    "golangci-lint-${VERSION#v}-linux-amd64/golangci-lint"
  rm -f "$TARBALL"
  chmod +x "$BIN"
  echo "$VERSION" > "$STAMP"
fi

# Default to the whole module; any args replace that entirely so callers
# can scope a run or pass --fix.
if [ "$#" -eq 0 ]; then
  set -- run ./... --timeout=5m
else
  set -- run "$@"
fi

echo "golangci-lint $VERSION (pinned via $VERSION_FILE)"
exec "$BIN" "$@"
