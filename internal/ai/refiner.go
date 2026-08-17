package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Provider keys. These identify a provider across every layer: the
// Refiner/Scorer Name() methods, the registry maps in cmd/server, the
// per-round provider column, and projects.scorer_provider with its CHECK
// constraint. Adding a fourth provider means touching all of those, so
// naming the set once here keeps the compiler in the loop for the Go
// half of it.
const (
	ProviderClaude = "claude"
	ProviderGemini = "gemini"
	ProviderOpenAI = "openai"
)

// SortedProviderKeys returns the keys present in a provider-keyed
// registry, in fixed display order.
//
// Order is pinned rather than map-iteration order so UI built from a
// registry — the debate AI-picker buttons, the project scorer dropdown —
// never shuffles between requests. Generic over the value type so the
// refiner and scorer registries share one definition instead of each
// carrying its own copy of the provider list.
func SortedProviderKeys[T any](registry map[string]T) []string {
	order := []string{ProviderClaude, ProviderGemini, ProviderOpenAI}
	keys := make([]string, 0, len(registry))
	for _, name := range order {
		if _, ok := registry[name]; ok {
			keys = append(keys, name)
		}
	}
	return keys
}

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

// Refiner request defaults. Centralized so every adapter uses the same
// Temperature and MaxTokens without drift; if we ever need to tune
// these for debate mode globally, the change lives in one place.
//
// refinerTemperature is low (0.3) — refactoring a spec should be
// stable, not creative; clicking the same AI button twice on the same
// seed should produce near-identical output.
//
// refinerMaxTokens is generous for any realistic feature description
// (20k chars ≈ 5k tokens) with headroom for the model to elaborate.
const (
	refinerMaxTokens   = 4096
	refinerTemperature = 0.3
)

// FinishReason constants. Adapters MUST map provider-specific stop
// reasons onto this set; the handler checks FinishReason == FinishReasonLength
// as a single equality to decide truncation rejections.
//
//   - FinishReasonStop          — model completed normally
//   - FinishReasonLength        — output truncated by token limit
//     (adapters map OpenAI's "length",
//     Anthropic's "max_tokens",
//     Gemini's FinishReasonMaxTokens to this)
//   - FinishReasonContentFilter — output blocked by safety filter
//   - FinishReasonToolCalls     — model requested tool invocation
//     (not used in debate mode but reserved
//     so the vocabulary is stable)
//
// Any provider-specific reason not in this set may be surfaced raw; the
// handler treats unknown FinishReason values as "stop-equivalent" and
// accepts the round (provided other validation passes).
const (
	FinishReasonStop          = "stop"
	FinishReasonLength        = "length"
	FinishReasonContentFilter = "content_filter"
	FinishReasonToolCalls     = "tool_calls"
)

// RefineOutput is the normalized response shape returned by every refiner.
// FinishReason uses the normalized vocabulary above; the CreateRound
// handler rejects rounds whose FinishReason == FinishReasonLength
// (spec §3.2) to prevent truncated AI output from silently overwriting a
// ticket description on approve.
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
//
// Each adapter uses its provider's own structured-output mechanism —
// Gemini a ResponseSchema, OpenAI a strict json_schema response_format,
// Anthropic a forced tool call — and normalizes the result into
// ScoreResult. Defensive clamps guarantee out-of-range values never
// reach the UI.
//
// Name and Model mirror Refiner. Model matters beyond audit: the startup
// pricing guard iterates the configured scorer registry calling Model()
// so an un-priced scorer surfaces as a loud WARNING instead of silently
// recording every score at $0 (issue #108). Implementations MUST be safe
// to call concurrently.
//
// Which scorer runs is configured per project (issue #63); see
// internal/handlers/debate.go's scorerForProject.
type Scorer interface {
	Name() string  // "claude" | "gemini" | "openai"
	Model() string // specific model ID used, for the pricing guard and audit
	Score(ctx context.Context, text string) (ScoreResult, error)
}

// Scorer request defaults, centralized like the refiner ones above so
// the three adapters cannot drift.
//
// scorerMaxTokens is small: the response is a {score, hours, reasoning}
// object whose reasoning is capped at one sentence by the prompt.
//
// scorerTemperature is pinned low so repeated scoring of the same text
// returns stable values — small drifts in the effort bar across
// re-scores are confusing to users.
const (
	scorerMaxTokens   = 512
	scorerTemperature = 0.2
)

// clampScoreFields bounds a model's raw score/hours into the range the
// UI can render: score 1..10, hours >= 1.
//
// This is shared rather than per-adapter because it is the ONLY
// enforcement for two of the three providers. Gemini applies the bounds
// server-side via ResponseSchema, but OpenAI's strict structured outputs
// reject numeric constraints such as minimum/maximum, and Anthropic tool
// input schemas do not enforce them either — so for those adapters a
// wayward model's 47 or 0 would otherwise reach the effort chip intact.
func clampScoreFields(score, hours int) (clampedScore, clampedHours int) {
	return min(max(score, 1), 10), max(hours, 1)
}

// scorePayload is the raw JSON shape every provider is asked to return.
// Shared so the three adapters cannot drift on field names — each
// provider enforces the shape by its own mechanism (Gemini
// ResponseSchema, OpenAI strict json_schema, Anthropic tool input
// schema), but they all unmarshal into this.
type scorePayload struct {
	Score     int    `json:"score"`
	Hours     int    `json:"hours"`
	Reasoning string `json:"reasoning"`
}

// parseScorePayload unmarshals a provider's structured output.
//
// The raw text is included in the error (truncated) because a parse
// failure here is almost always a provider returning prose or a fenced
// code block instead of bare JSON, and the first 200 characters make
// that immediately obvious in a log line. Callers prefix the provider
// name.
func parseScorePayload(raw string) (scorePayload, error) {
	var out scorePayload
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return scorePayload{}, fmt.Errorf("JSON parse: %w (raw: %q)", err, truncateForError(raw))
	}
	return out, nil
}

// truncateForError bounds raw provider output included in error
// messages, so a runaway response can't dump kilobytes into the logs.
//
// The cap is in bytes, so the cut can land in the middle of a multi-byte
// character — client feature text is frequently non-ASCII, and a
// provider echoing it back in a malformed reply is exactly when this
// fires. ToValidUTF8 drops the partial trailing rune so the message
// stays well-formed.
func truncateForError(s string) string {
	const maxLen = 200
	if len(s) <= maxLen {
		return s
	}
	return strings.ToValidUTF8(s[:maxLen], "") + "…"
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
