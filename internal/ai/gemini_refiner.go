package ai

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// GeminiRefiner adapts the Gemini API to the debate Refiner interface.
// Reuses the existing *genai.Client via constructor injection so the
// chat assistant client and the debate client share network/auth state
// without coupling their code paths.
type GeminiRefiner struct {
	client *GeminiClient
	model  string
}

// NewGeminiRefiner constructs a Refiner over the shared GeminiClient.
// The model string should be one of ai.ModelGemini* constants so the
// pricing-table lookup finds a rate.
func NewGeminiRefiner(c *GeminiClient, model string) *GeminiRefiner {
	return &GeminiRefiner{client: c, model: model}
}

func (r *GeminiRefiner) Name() string  { return "gemini" }
func (r *GeminiRefiner) Model() string { return r.model }

// Refine sends one refactoring request and maps the response back to
// the shared RefineOutput contract. Empty outputs fail at the adapter
// layer (defense in depth; the handler also rejects via MinOutputLen).
// FinishReason is normalized via mapGeminiFinishReason so the handler's
// single-equality truncation check catches MAX_TOKENS uniformly with
// the other two providers.
func (r *GeminiRefiner) Refine(ctx context.Context, in RefineInput) (RefineOutput, error) {
	if r.client == nil || r.client.client == nil {
		return RefineOutput{}, fmt.Errorf("gemini refiner: client not configured")
	}

	contents := []*genai.Content{
		genai.NewContentFromText(
			buildRefineUserPrompt(in.CurrentText, in.Feedback),
			genai.RoleUser,
		),
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: resolveSystemPrompt(in.SystemPrompt)}},
		},
		Temperature: genai.Ptr[float32](0.3),
	}

	resp, err := r.client.client.Models.GenerateContent(ctx, r.model, contents, config)
	if err != nil {
		return RefineOutput{}, fmt.Errorf("gemini refine: %w", err)
	}

	// Trim BEFORE the empty-check so whitespace-only responses (just
	// "\n" or "   ") are rejected too — otherwise a model returning
	// only whitespace would pass the empty-string guard and then be
	// TrimSpace'd to "" before the caller sees it, defeating the
	// defense-in-depth we document against silent data loss.
	text := strings.TrimSpace(extractGeminiText(resp))
	if text == "" {
		return RefineOutput{}, fmt.Errorf("gemini refine: empty response from model %s", r.model)
	}

	finish := FinishReasonStop
	if len(resp.Candidates) > 0 {
		finish = mapGeminiFinishReason(resp.Candidates[0].FinishReason)
	}

	inputTok, outputTok := 0, 0
	if resp.UsageMetadata != nil {
		inputTok = int(resp.UsageMetadata.PromptTokenCount)
		outputTok = int(resp.UsageMetadata.CandidatesTokenCount)
	}

	return RefineOutput{
		Text:         text,
		FinishReason: finish,
		Usage: RefineUsage{
			InputTokens:  inputTok,
			OutputTokens: outputTok,
			CostMicros:   ComputeCostMicros(r.model, inputTok, outputTok),
			Model:        r.model,
		},
	}, nil
}

// extractGeminiText walks the first candidate's Content.Parts and
// concatenates text segments. Parts from Gemini are contiguous chunks
// of the same response (especially during long outputs), not separate
// logical lines — joining with "" instead of "\n" preserves the
// original text integrity. Critical for the scorer: a JSON response
// split across parts would break json.Unmarshal if we inserted
// newlines inside a string value.
func extractGeminiText(resp *genai.GenerateContentResponse) string {
	if resp == nil || len(resp.Candidates) == 0 {
		return ""
	}
	cand := resp.Candidates[0]
	if cand == nil || cand.Content == nil {
		return ""
	}
	var parts []string
	for _, p := range cand.Content.Parts {
		if p == nil {
			continue
		}
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "")
}

// mapGeminiFinishReason normalizes Gemini's FinishReason enum to the
// Refiner FinishReason contract. Unknown values route through a
// substring check (matching the Anthropic adapter's defensive policy)
// so future SDK additions with truncation semantics still reject
// downstream without an SDK bump.
func mapGeminiFinishReason(reason genai.FinishReason) string {
	switch reason {
	case genai.FinishReasonStop, genai.FinishReasonUnspecified:
		return FinishReasonStop
	case genai.FinishReasonMaxTokens:
		return FinishReasonLength
	case genai.FinishReasonSafety, genai.FinishReasonBlocklist,
		genai.FinishReasonProhibitedContent, genai.FinishReasonSPII,
		genai.FinishReasonImageSafety, genai.FinishReasonImageProhibitedContent:
		return FinishReasonContentFilter
	case genai.FinishReasonUnexpectedToolCall, genai.FinishReasonMalformedFunctionCall:
		return FinishReasonToolCalls
	default:
		raw := strings.ToLower(string(reason))
		if strings.Contains(raw, "max") ||
			strings.Contains(raw, "length") ||
			strings.Contains(raw, "exceeded") ||
			strings.Contains(raw, "truncat") {
			return FinishReasonLength
		}
		return string(reason)
	}
}
