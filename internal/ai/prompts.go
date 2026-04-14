package ai

import (
	_ "embed"
	"strings"
)

// debateRefineSystemPrompt is the canonical refiner system prompt,
// embedded from internal/ai/prompts/debate_system.md so it ships with
// the binary and can be edited as markdown without touching Go code.
// All three refiner adapters use this as the default when
// RefineInput.SystemPrompt is empty.
//
//go:embed prompts/debate_system.md
var debateRefineSystemPrompt string

// debateScoreSystemPrompt is the canonical scorer system prompt,
// embedded from internal/ai/prompts/debate_score_system.md. Used by
// GeminiScorer (Task 5) to drive structured-output JSON results.
//
//go:embed prompts/debate_score_system.md
var debateScoreSystemPrompt string

// resolveSystemPrompt returns the caller-supplied prompt if non-empty,
// otherwise the embedded fallback. Every refiner adapter MUST route
// through this rather than forwarding in.SystemPrompt verbatim, so an
// empty SystemPrompt (documented as valid in RefineInput) doesn't result
// in an unsystematic AI call.
func resolveSystemPrompt(s string) string {
	if s == "" {
		return debateRefineSystemPrompt
	}
	return s
}

// buildRefineUserPrompt wraps the current description and optional user
// feedback in explicit delimited blocks. This is a standard prompt-
// injection containment pattern — the model is instructed (via the
// system prompt) to treat everything inside the blocks as input data,
// not as instructions.
//
// Shared across all three refiner adapters (Anthropic, Gemini, OpenAI)
// so injection containment is uniform. Lives in this file rather than
// in a vendor-specific adapter because the helper has no vendor-specific
// concerns — it's pure string construction that every adapter uses
// identically before passing the result to its SDK's user-message API.
func buildRefineUserPrompt(currentText, feedback string) string {
	var sb strings.Builder
	sb.WriteString("<<<CURRENT_DESCRIPTION>>>\n")
	sb.WriteString(currentText)
	sb.WriteString("\n<<<END_CURRENT_DESCRIPTION>>>\n\n")
	if feedback != "" {
		sb.WriteString("<<<USER_FEEDBACK>>>\n")
		sb.WriteString(feedback)
		sb.WriteString("\n<<<END_USER_FEEDBACK>>>\n\n")
	}
	sb.WriteString("Refactor the description above. Return only the new description text.")
	return sb.String()
}
