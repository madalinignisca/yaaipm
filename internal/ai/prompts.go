package ai

import "strings"

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
