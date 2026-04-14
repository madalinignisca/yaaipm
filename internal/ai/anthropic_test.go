package ai

import (
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

// Compile-time interface assertion — catches signature drift before any
// test runs. If AnthropicRefiner ever stops satisfying Refiner this file
// fails to build.
var _ Refiner = (*AnthropicRefiner)(nil)

func TestAnthropicRefiner_NameAndModel(t *testing.T) {
	r := &AnthropicRefiner{client: nil, model: ModelClaudeSonnet46}
	if r.Name() != "claude" {
		t.Errorf("Name() = %q, want claude", r.Name())
	}
	if r.Model() != ModelClaudeSonnet46 {
		t.Errorf("Model() = %q, want %s", r.Model(), ModelClaudeSonnet46)
	}
}

func TestAnthropicRefiner_RefineWithoutClientErrors(t *testing.T) {
	r := &AnthropicRefiner{client: nil, model: ModelClaudeSonnet46}
	_, err := r.Refine(t.Context(), RefineInput{CurrentText: "x"})
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestMapAnthropicStopReason(t *testing.T) {
	cases := []struct {
		in   anthropic.StopReason
		want string
	}{
		{anthropic.StopReasonEndTurn, FinishReasonStop},
		{anthropic.StopReasonStopSequence, FinishReasonStop},
		{anthropic.StopReasonMaxTokens, FinishReasonLength},
		{anthropic.StopReasonToolUse, FinishReasonToolCalls},
		{anthropic.StopReasonRefusal, FinishReasonContentFilter},
		{anthropic.StopReasonPauseTurn, string(anthropic.StopReasonPauseTurn)}, // surfaced raw
	}
	for _, c := range cases {
		got := mapAnthropicStopReason(c.in)
		if got != c.want {
			t.Errorf("mapAnthropicStopReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildRefineUserPrompt_NoFeedback(t *testing.T) {
	got := buildRefineUserPrompt("the description", "")
	if !strings.Contains(got, "<<<CURRENT_DESCRIPTION>>>") {
		t.Error("missing current-description delimiter")
	}
	if !strings.Contains(got, "the description") {
		t.Error("missing current description text")
	}
	if strings.Contains(got, "<<<USER_FEEDBACK>>>") {
		t.Error("feedback block must be omitted when feedback is empty")
	}
	if !strings.Contains(got, "Return only the new description text.") {
		t.Error("missing terminal instruction")
	}
}

func TestBuildRefineUserPrompt_WithFeedback(t *testing.T) {
	got := buildRefineUserPrompt("desc", "make it shorter")
	for _, want := range []string{
		"<<<CURRENT_DESCRIPTION>>>",
		"desc",
		"<<<USER_FEEDBACK>>>",
		"make it shorter",
		"<<<END_USER_FEEDBACK>>>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected prompt to contain %q, got: %s", want, got)
		}
	}
}
