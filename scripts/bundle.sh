#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."

# Concatenate all JS into a single bundle.
# Order matters:
#   1. HTMX (no dependencies)
#   2. Marked (markdown parser, used by assistant)
#   3. DOMPurify (HTML sanitizer, used by assistant)
#   4. EasyMDE (markdown editor, used on brief/ticket pages)
#   5. Mermaid (diagram renderer)
#   6. App code (webauthn, gantt, assistant — define Alpine components)
#   7. Init (mermaid initialization + htmx:afterSettle hook)
#   8. Alpine.js MUST BE LAST (auto-starts on load, needs all components defined)

# Each file gets a ";\n" appended to prevent cross-file parse errors.
# Raw cat is unsafe: if file A ends without a newline (e.g. a // comment)
# and file B starts with /* or similar, the two merge into broken syntax.
: > static/js/bundle.js
for f in \
  static/js/vendor/htmx.min.js \
  static/js/vendor/marked.min.js \
  static/js/vendor/purify.min.js \
  static/js/vendor/easymde.min.js \
  static/js/vendor/mermaid.min.js \
  static/js/app/webauthn.js \
  static/js/app/gantt.js \
  static/js/app/assistant.js \
  static/js/app/init.js \
  static/js/vendor/alpine.min.js
do
  if [ ! -f "$f" ]; then
    echo "ERROR: missing file: $f" >&2
    exit 1
  fi
  cat "$f" >> static/js/bundle.js
  printf ';\n' >> static/js/bundle.js
done

SIZE=$(wc -c < static/js/bundle.js | tr -d ' ')
echo "static/js/bundle.js  ${SIZE} bytes"
