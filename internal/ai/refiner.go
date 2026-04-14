package ai

import "context"

// Refiner refactors a feature description for one round of debate mode.
// Implementations MUST be safe to call concurrently.
//
// This interface is deliberately tiny — we do not abstract over the full
// vendor SDK surface here. Each adapter (Anthropic, Gemini, OpenAI) converts
// its provider-specific response into the shared RefineOutput, including
// a normalized FinishReason so the handler can detect truncation uniformly.
//
// See docs/superpowers/specs/2026-04-14-feature-debate-mode-design.md §3.2
// for the design rationale.
type Refiner interface {
	Name() string  // "claude" | "gemini" | "openai"
	Model() string // specific model ID used, for per-round audit
	Refine(ctx context.Context, in RefineInput) (RefineOutput, error)
}

// RefineInput is the per-round input a refiner operates on.
// SystemPrompt is optional; adapters fall back to their embedded default if empty.
type RefineInput struct {
	CurrentText  string
	Feedback     string
	SystemPrompt string
}

// RefineOutput is the normalized response shape returned by every refiner.
// FinishReason uses a small normalized vocabulary: "stop" | "length" |
// "content_filter" | "tool_calls", or a provider-specific string for
// anything outside that set. The CreateRound handler rejects rounds whose
// FinishReason is "length" or "max_tokens" (spec §3.2) to prevent truncated
// AI output from silently overwriting a ticket description on approve.
type RefineOutput struct {
	Text         string
	Usage        RefineUsage
	FinishReason string
}

// RefineUsage normalizes token counts and cost across vendors.
// CostMicros is in millionths of USD (1 cent = 10_000 micros) — see
// pricing.go's costCentsDelta for the cent-boundary conversion that bounds
// rounding error to <1 cent per debate.
type RefineUsage struct {
	InputTokens  int
	OutputTokens int
	CostMicros   int64
	Model        string
}

// Scorer judges the complexity of a feature description.
// v1 uses GeminiScorer with structured output so the JSON shape is
// schema-enforced; defensive clamps in the adapter guarantee
// out-of-range values never reach the UI.
type Scorer interface {
	Score(ctx context.Context, text string) (ScoreResult, error)
}

// ScoreResult is the structured scorer output consumed by the accept flow
// (spec §4.3). Score is 1..10, Hours is total human-hours estimate,
// Reasoning is one sentence describing the biggest risk or scope driver.
type ScoreResult struct {
	Score     int
	Hours     int
	Reasoning string
	Usage     RefineUsage
}
