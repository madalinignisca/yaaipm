package ai

import (
	"strings"
	"testing"
)

func TestGeminiScorer_ScoreWithoutClientErrors(t *testing.T) {
	s := &GeminiScorer{client: nil, model: ModelGeminiFlash}
	_, err := s.Score(t.Context(), "some text")
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDebateScoreSystemPrompt_Embedded(t *testing.T) {
	if debateScoreSystemPrompt == "" {
		t.Fatal("debateScoreSystemPrompt must be embedded and non-empty")
	}
	for _, want := range []string{"Return JSON", "score", "hours", "reasoning"} {
		if !strings.Contains(debateScoreSystemPrompt, want) {
			t.Errorf("embedded scorer prompt missing %q", want)
		}
	}
}

func TestDebateRefineSystemPrompt_Embedded(t *testing.T) {
	if debateRefineSystemPrompt == "" {
		t.Fatal("debateRefineSystemPrompt must be embedded and non-empty")
	}
	for _, want := range []string{"senior product engineer", "<<<CURRENT_DESCRIPTION>>>"} {
		if !strings.Contains(debateRefineSystemPrompt, want) {
			t.Errorf("embedded refiner prompt missing %q", want)
		}
	}
}
