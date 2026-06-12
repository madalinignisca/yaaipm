package diff

import (
	"strings"
	"testing"
)

func TestComputeUnified_IdenticalHasNoChanges(t *testing.T) {
	got := ComputeUnified("a\nb\n", "a\nb\n")
	if strings.Contains(got, "+") || strings.Contains(got, "-") {
		t.Errorf("identical inputs produced add/del lines: %q", got)
	}
}

func TestComputeUnified_AddLine(t *testing.T) {
	got := ComputeUnified("a\n", "a\nb\n")
	if !strings.Contains(got, "+b") {
		t.Errorf("missing add line prefix: %q", got)
	}
}

func TestComputeUnified_DeleteLine(t *testing.T) {
	got := ComputeUnified("a\nb\n", "a\n")
	if !strings.Contains(got, "-b") {
		t.Errorf("missing del line prefix: %q", got)
	}
}

func TestComputeUnified_ReplaceLine(t *testing.T) {
	got := ComputeUnified("a\nb\nc\n", "a\nB\nc\n")
	if !strings.Contains(got, "-b") || !strings.Contains(got, "+B") {
		t.Errorf("replace should produce both -b and +B, got %q", got)
	}
}

func TestRenderHTML_EscapesScriptTags(t *testing.T) {
	unified := "+<script>alert(1)</script>\n"
	got := string(RenderHTML(unified))
	if strings.Contains(got, "<script>alert(1)</script>") {
		t.Errorf("raw <script> leaked through, got: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped form, got: %q", got)
	}
}

func TestRenderHTML_EscapesAttributeInjection(t *testing.T) {
	unified := `+<img src=x onerror="alert(1)">` + "\n"
	got := string(RenderHTML(unified))
	if strings.Contains(got, `onerror="alert(1)"`) {
		t.Errorf("onerror attribute leaked unescaped: %q", got)
	}
}

func TestRenderHTML_ClassesAppliedCorrectly(t *testing.T) {
	unified := "-gone\n+added\n same\n"
	got := string(RenderHTML(unified))
	if !strings.Contains(got, `class="diff-del"`) {
		t.Error("missing diff-del class")
	}
	if !strings.Contains(got, `class="diff-add"`) {
		t.Error("missing diff-add class")
	}
	if !strings.Contains(got, `class="diff-ctx"`) {
		t.Error("missing diff-ctx class")
	}
}

func TestRenderHTML_EmptyInput(t *testing.T) {
	got := string(RenderHTML(""))
	if !strings.Contains(got, `<pre class="diff-block">`) {
		t.Errorf("empty input should still produce the outer wrapper: %q", got)
	}
}

func TestRenderInlineHTML_WordLevelInsert(t *testing.T) {
	got := string(RenderInlineHTML("The quick fox jumps.", "The quick brown fox jumps."))
	if !strings.Contains(got, `<ins class="diff-ins">`) {
		t.Fatalf("expected <ins> for insertion, got: %s", got)
	}
	if strings.Contains(got, "<del") {
		t.Fatalf("pure insertion must not produce <del>, got: %s", got)
	}
	if !strings.Contains(got, "brown") {
		t.Fatalf("inserted word missing, got: %s", got)
	}
}

func TestRenderInlineHTML_WordLevelDelete(t *testing.T) {
	got := string(RenderInlineHTML("uses basic logging only today", "uses logging today"))
	if !strings.Contains(got, `<del class="diff-del">`) {
		t.Fatalf("expected <del> for deletion, got: %s", got)
	}
}

func TestRenderInlineHTML_EscapesScriptTags(t *testing.T) {
	got := string(RenderInlineHTML("before", `before <script>alert(1)</script>`))
	if strings.Contains(got, "<script>") {
		t.Fatalf("script tag not escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag, got: %s", got)
	}
}

func TestRenderInlineHTML_EscapesAttributeInjection(t *testing.T) {
	got := string(RenderInlineHTML("a", `a " onmouseover="alert(1)`))
	if strings.Contains(got, `" onmouseover="`) {
		t.Fatalf("attribute injection survived: %s", got)
	}
}

func TestRenderInlineHTML_PreservesNewlines(t *testing.T) {
	// Newlines must survive escaping verbatim — the container renders
	// with white-space: pre-wrap, so "\n" is the line-break mechanism.
	got := string(RenderInlineHTML("## Title\n- item one", "## Title\n- item one\n- item two"))
	if !strings.Contains(got, "\n") {
		t.Fatalf("newlines lost — pre-wrap rendering will collapse lines: %s", got)
	}
	if strings.Contains(got, "<br") {
		t.Fatalf("renderer must not invent <br> tags: %s", got)
	}
}

func TestRenderInlineHTML_Unicode(t *testing.T) {
	got := string(RenderInlineHTML("naïve café", "naïve café ☕ déjà"))
	if !strings.Contains(got, "☕") || !strings.Contains(got, "déjà") {
		t.Fatalf("unicode mangled: %s", got)
	}
}

func TestRenderInlineHTML_IdenticalInputs(t *testing.T) {
	got := string(RenderInlineHTML("same text", "same text"))
	if strings.Contains(got, "<ins") || strings.Contains(got, "<del") {
		t.Fatalf("identical inputs must produce no ins/del: %s", got)
	}
}

func TestRenderInlineHTML_EmptyInputs(t *testing.T) {
	got := string(RenderInlineHTML("", ""))
	if !strings.Contains(got, `class="diff-inline"`) {
		t.Fatalf("expected empty wrapper, got: %s", got)
	}
}
