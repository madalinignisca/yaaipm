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
