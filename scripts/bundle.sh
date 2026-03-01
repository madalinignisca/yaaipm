#!/bin/sh
set -eu
cd "$(dirname "$0")/.."

# Concatenate all JS into a single bundle.
# Order matters:
#   1. HTMX (no dependencies)
#   2. Marked (markdown parser, used by assistant)
#   3. EasyMDE (markdown editor, used on brief/ticket pages)
#   4. Mermaid (diagram renderer)
#   5. App code (webauthn, gantt, assistant — define Alpine components)
#   6. Init (mermaid initialization + htmx:afterSettle hook)
#   7. Alpine.js MUST BE LAST (auto-starts on load, needs all components defined)

cat \
  static/js/vendor/htmx.min.js \
  static/js/vendor/marked.min.js \
  static/js/vendor/easymde.min.js \
  static/js/vendor/mermaid.min.js \
  static/js/app/webauthn.js \
  static/js/app/gantt.js \
  static/js/app/assistant.js \
  static/js/app/init.js \
  static/js/vendor/alpine.min.js \
  > static/js/bundle.js

SIZE=$(wc -c < static/js/bundle.js | tr -d ' ')
echo "static/js/bundle.js  ${SIZE} bytes"
