package ai

import (
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// Compile-time interface assertion — catches signature drift at build time.
var _ Refiner = (*OpenAIRefiner)(nil)

func TestOpenAIRefiner_NameAndModel(t *testing.T) {
	c := NewOpenAIClient("fake-key", ModelGPT5Mini)
	r := NewOpenAIRefiner(c)
	if r.Name() != "openai" {
		t.Errorf("Name() = %q, want openai", r.Name())
	}
	if r.Model() != ModelGPT5Mini {
		t.Errorf("Model() = %q, want %s", r.Model(), ModelGPT5Mini)
	}
}

func TestOpenAIRefiner_ModelEmptyWhenClientNil(t *testing.T) {
	r := &OpenAIRefiner{c: nil}
	if r.Model() != "" {
		t.Errorf("Model() with nil client should be empty, got %q", r.Model())
	}
}

func TestOpenAIRefiner_RefineWithoutClientErrors(t *testing.T) {
	r := &OpenAIRefiner{c: nil}
	_, err := r.Refine(t.Context(), RefineInput{CurrentText: "x"})
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestMapOpenAIFinishReason(t *testing.T) {
	cases := []struct {
		in   openai.FinishReason
		want string
	}{
		{openai.FinishReasonStop, FinishReasonStop},
		{openai.FinishReasonLength, FinishReasonLength},
		{openai.FinishReasonContentFilter, FinishReasonContentFilter},
		{openai.FinishReasonToolCalls, FinishReasonToolCalls},
		{openai.FinishReasonFunctionCall, FinishReasonToolCalls}, // merged
		{openai.FinishReasonNull, FinishReasonStop},              // treated as stop
		{openai.FinishReason(""), FinishReasonStop},              // empty → stop
		// Future truncation-shaped reasons → FinishReasonLength.
		{openai.FinishReason("context_length_exceeded"), FinishReasonLength},
		{openai.FinishReason("max_completion_tokens"), FinishReasonLength},
		// Truly unknown → surfaced raw.
		{openai.FinishReason("something_new"), "something_new"},
	}
	for _, c := range cases {
		got := mapOpenAIFinishReason(c.in)
		if got != c.want {
			t.Errorf("mapOpenAIFinishReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
