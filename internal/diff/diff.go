// Package diff provides server-side unified diff computation and HTML
// rendering for the Feature Debate Mode. Output is sanitized for safe
// embedding in templates: RenderHTML HTML-escapes every line, so AI
// output (which flows through this path on every debate round) cannot
// smuggle script tags or attribute injection into the DOM.
package diff

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// ComputeUnified returns a line-level unified diff between two markdown
// strings in a GitHub-style format: lines prefixed with " ", "+", or "-"
// depending on whether they are context, additions, or deletions.
//
// We use diffmatchpatch's line-mode (DiffLinesToChars/DiffCharsToLines)
// rather than its default character-level mode because markdown
// refactors are naturally line-oriented; a character diff would
// introduce noise from reflow and whitespace tweaks that adds no signal
// to a human reviewer.
func ComputeUnified(before, after string) string {
	dmp := diffmatchpatch.New()
	a, b, lines := dmp.DiffLinesToChars(before, after)
	diffs := dmp.DiffMain(a, b, false)
	diffs = dmp.DiffCharsToLines(diffs, lines)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var sb strings.Builder
	for _, d := range diffs {
		var prefix rune
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			prefix = ' '
		case diffmatchpatch.DiffInsert:
			prefix = '+'
		case diffmatchpatch.DiffDelete:
			prefix = '-'
		}
		for _, line := range strings.SplitAfter(d.Text, "\n") {
			if line == "" {
				continue
			}
			sb.WriteRune(prefix)
			sb.WriteString(line)
		}
	}
	return sb.String()
}

// RenderHTML converts unified diff text into sanitized HTML. Every line
// is HTML-escaped via template.HTMLEscapeString, then wrapped in a
// <span> with a class reflecting its prefix (add / del / ctx). The
// trailing <pre><code>...</code></pre> wrapper lets the CSS preserve
// whitespace and apply the three-band color scheme.
//
// Because every line goes through HTMLEscapeString, AI-generated text
// containing <script>, <img onerror=...>, or other HTML fragments is
// rendered as inert text rather than parsed by the browser. This is
// the last line of defense in a chain that also includes the handler's
// output validation (§3.2) and the model-layer UpdateTicketDescription
// guard (§3.3).
func RenderHTML(unified string) template.HTML {
	var sb strings.Builder
	sb.WriteString(`<pre class="diff-block"><code>`)
	for _, line := range strings.SplitAfter(unified, "\n") {
		if line == "" {
			continue
		}
		class := "diff-ctx"
		switch {
		case strings.HasPrefix(line, "+"):
			class = "diff-add"
		case strings.HasPrefix(line, "-"):
			class = "diff-del"
		}
		body := strings.TrimRight(line, "\n")
		fmt.Fprintf(&sb, `<span class=%q>%s</span>`+"\n",
			class, template.HTMLEscapeString(body))
	}
	sb.WriteString(`</code></pre>`)
	// Safety audit: every user/AI-derived value (body) is routed through
	// template.HTMLEscapeString; the only literal HTML in sb is the
	// handful of tags and class attributes hardcoded above. No untrusted
	// input reaches the output unescaped, so this template.HTML cast is
	// sound. See TestRenderHTML_EscapesScriptTags and
	// TestRenderHTML_EscapesAttributeInjection for the pinned proofs.
	return template.HTML(sb.String()) // nosemgrep: go.lang.security.audit.xss.template-html-does-not-escape.unsafe-template-type
}
