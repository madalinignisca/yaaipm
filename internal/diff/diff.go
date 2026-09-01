// Package diff provides server-side unified diff computation and inline
// HTML rendering for the Feature Debate Mode. ComputeUnified is persisted
// to feature_debate_rounds.diff_unified for the audit trail. RenderInlineHTML
// produces word-level prose diffs for the debate suggestion panel's "What
// changed" tab. Output is sanitized for safe embedding in templates.
package diff

import (
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

// RenderInlineHTML diffs before→after at word/phrase granularity and
// returns sanitized HTML: unchanged text plain, insertions wrapped in
// <ins class="diff-ins">, deletions in <del class="diff-del">. The
// container div uses white-space: pre-wrap (see tw-input.css) so the
// escaped newlines preserve markdown's line structure without any <br>
// rewriting here.
//
// DiffCleanupSemantic merges character-level noise into human-readable
// runs — that is what makes prose diffs legible vs. the line-level
// unified output of ComputeUnified (kept for the audit trail).
//
// Every text segment is routed through template.HTMLEscapeString; the
// only literal HTML is the hardcoded wrapper/ins/del tags, so the
// template.HTML cast is sound. Pinned by
// TestRenderInlineHTML_EscapesScriptTags / _EscapesAttributeInjection.
func RenderInlineHTML(before, after string) template.HTML {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(before, after, false)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var sb strings.Builder
	sb.WriteString(`<div class="diff-inline">`)
	for _, d := range diffs {
		text := template.HTMLEscapeString(d.Text)
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			sb.WriteString(text)
		case diffmatchpatch.DiffInsert:
			sb.WriteString(`<ins class="diff-ins">`)
			sb.WriteString(text)
			sb.WriteString(`</ins>`)
		case diffmatchpatch.DiffDelete:
			sb.WriteString(`<del class="diff-del">`)
			sb.WriteString(text)
			sb.WriteString(`</del>`)
		}
	}
	sb.WriteString(`</div>`)
	return template.HTML(sb.String()) // nosemgrep: go.lang.security.audit.xss.template-html-does-not-escape.unsafe-template-type
}
