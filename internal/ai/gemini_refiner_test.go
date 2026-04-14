package ai

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// Compile-time interface assertions.
var (
	_ Refiner = (*GeminiRefiner)(nil)
	_ Scorer  = (*GeminiScorer)(nil)
)

func TestGeminiRefiner_NameAndModel(t *testing.T) {
	r := &GeminiRefiner{client: nil, model: ModelGeminiFlash}
	if r.Name() != "gemini" {
		t.Errorf("Name() = %q, want gemini", r.Name())
	}
	if r.Model() != ModelGeminiFlash {
		t.Errorf("Model() = %q, want %s", r.Model(), ModelGeminiFlash)
	}
}

func TestGeminiRefiner_RefineWithoutClientErrors(t *testing.T) {
	r := &GeminiRefiner{client: nil, model: ModelGeminiFlash}
	_, err := r.Refine(t.Context(), RefineInput{CurrentText: "x"})
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestMapGeminiFinishReason(t *testing.T) {
	cases := []struct {
		in   genai.FinishReason
		want string
	}{
		{genai.FinishReasonStop, FinishReasonStop},
		{genai.FinishReasonUnspecified, FinishReasonStop},
		{genai.FinishReasonMaxTokens, FinishReasonLength},
		{genai.FinishReasonSafety, FinishReasonContentFilter},
		{genai.FinishReasonBlocklist, FinishReasonContentFilter},
		{genai.FinishReasonProhibitedContent, FinishReasonContentFilter},
		{genai.FinishReasonSPII, FinishReasonContentFilter},
		{genai.FinishReasonImageSafety, FinishReasonContentFilter},
		{genai.FinishReasonImageProhibitedContent, FinishReasonContentFilter},
		{genai.FinishReasonUnexpectedToolCall, FinishReasonToolCalls},
		{genai.FinishReasonMalformedFunctionCall, FinishReasonToolCalls},
		{genai.FinishReasonRecitation, string(genai.FinishReasonRecitation)}, // surfaced raw
		{genai.FinishReasonOther, string(genai.FinishReasonOther)},           // surfaced raw
		// Future truncation-shaped reasons → FinishReasonLength via substring
		// detection so the handler's single-equality check catches them
		// without an SDK bump.
		{genai.FinishReason("context_length_exceeded"), FinishReasonLength},
		{genai.FinishReason("max_output_tokens"), FinishReasonLength},
	}
	for _, c := range cases {
		got := mapGeminiFinishReason(c.in)
		if got != c.want {
			t.Errorf("mapGeminiFinishReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractGeminiText_NilAndEmptyShapes(t *testing.T) {
	if got := extractGeminiText(nil); got != "" {
		t.Errorf("nil response → %q, want empty", got)
	}
	empty := &genai.GenerateContentResponse{}
	if got := extractGeminiText(empty); got != "" {
		t.Errorf("empty response → %q, want empty", got)
	}

	// Single-part text response.
	singlePart := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []*genai.Part{{Text: "hello"}}}},
		},
	}
	if got := extractGeminiText(singlePart); got != "hello" {
		t.Errorf("single-part → %q, want hello", got)
	}

	// Multi-part with a nil part + function call + text (function_call has no
	// Text field so it contributes nothing; nil parts are skipped). Parts
	// are concatenated with "" (empty separator) — Gemini returns them as
	// contiguous chunks of one response, not as separate lines; inserting
	// newlines would break JSON parsing for the scorer use case.
	multi := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []*genai.Part{
				nil,
				{Text: "one"},
				{Text: "two"},
			}}},
		},
	}
	if got := extractGeminiText(multi); got != "onetwo" {
		t.Errorf("multi-part → %q, want 'onetwo'", got)
	}

	// Specific regression for the scorer JSON-split case: if Gemini splits
	// a JSON payload across parts inside a string value, concatenating with
	// "" preserves it as valid JSON; concatenating with "\n" would insert
	// an unescaped newline mid-string and break json.Unmarshal.
	jsonSplit := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []*genai.Part{
				{Text: `{"score": 7, "hours": 20, "reasoning": "needs sub`},
				{Text: `tasks from start"}`},
			}}},
		},
	}
	got := extractGeminiText(jsonSplit)
	if got != `{"score": 7, "hours": 20, "reasoning": "needs subtasks from start"}` {
		t.Errorf("json-split → %q (would break json.Unmarshal)", got)
	}
}
