package render

import (
	"html/template"
	"strings"
	"testing"
)

// TestFuncMap_RenderInlineDiff verifies that "renderInlineDiff" is
// registered in the static FuncMap (entries with no closed-over
// NewEngine locals) and that it delegates to diff.RenderInlineHTML,
// producing <ins> markup for an added word.
func TestFuncMap_RenderInlineDiff(t *testing.T) {
	fm := staticFuncMap()
	raw, ok := fm["renderInlineDiff"]
	if !ok {
		t.Fatal("renderInlineDiff not registered in staticFuncMap")
	}
	fn, ok := raw.(func(before, after string) template.HTML)
	if !ok {
		t.Fatalf("renderInlineDiff has unexpected type %T", raw)
	}
	got := string(fn("a b", "a c b"))
	if !strings.Contains(got, "<ins") {
		t.Fatalf("expected <ins> markup for insertion, got: %s", got)
	}
}
