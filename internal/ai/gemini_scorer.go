package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/genai"
)

// GeminiScorer adapts Gemini's structured-output feature to the debate
// Scorer interface. Uses ResponseSchema to enforce the {score, hours,
// reasoning} JSON shape server-side — no regex parsing, and the
// defensive clamps below protect the UI even if the model returns a
// value just outside the schema bounds.
type GeminiScorer struct {
	client *GeminiClient
	model  string
}

// NewGeminiScorer constructs a Scorer over the shared GeminiClient.
// The model string should be a flash-class Gemini model for cost — the
// v1 handler always uses this scorer regardless of which provider ran
// the refiner.
func NewGeminiScorer(c *GeminiClient, model string) *GeminiScorer {
	return &GeminiScorer{client: c, model: model}
}

// Score returns {score, hours, reasoning} for the given description.
// Temperature is pinned low (0.2) so repeated scoring of the same text
// returns stable values; small drifts in the effort bar across re-scores
// are confusing to users.
func (s *GeminiScorer) Score(ctx context.Context, text string) (ScoreResult, error) {
	if s.client == nil || s.client.client == nil {
		return ScoreResult{}, fmt.Errorf("gemini scorer: client not configured")
	}

	contents := []*genai.Content{
		genai.NewContentFromText(text, genai.RoleUser),
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: debateScoreSystemPrompt}},
		},
		Temperature:      genai.Ptr[float32](0.2),
		ResponseMIMEType: "application/json",
		ResponseSchema: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"score":     {Type: genai.TypeInteger, Minimum: genai.Ptr(1.0), Maximum: genai.Ptr(10.0)},
				"hours":     {Type: genai.TypeInteger, Minimum: genai.Ptr(1.0)},
				"reasoning": {Type: genai.TypeString, MaxLength: genai.Ptr[int64](250)},
			},
			Required: []string{"score", "hours", "reasoning"},
		},
	}

	resp, err := s.client.client.Models.GenerateContent(ctx, s.model, contents, config)
	if err != nil {
		return ScoreResult{}, fmt.Errorf("gemini scorer: %w", err)
	}

	raw := extractGeminiText(resp)
	if raw == "" {
		return ScoreResult{}, fmt.Errorf("gemini scorer: empty response from model %s", s.model)
	}

	var out struct {
		Score     int    `json:"score"`
		Hours     int    `json:"hours"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ScoreResult{}, fmt.Errorf("gemini scorer JSON parse: %w (raw: %q)", err, raw)
	}

	// Defensive clamps. ResponseSchema should enforce the bounds server-
	// side, but a wayward model or SDK quirk could still return an
	// out-of-range value. Clamping means a bad response never reaches
	// the CSS calc() in debate_sidebar.html with a negative or >100%
	// pointer position.
	score := min(max(out.Score, 1), 10)
	hours := max(out.Hours, 1)

	inputTok, outputTok := 0, 0
	if resp.UsageMetadata != nil {
		inputTok = int(resp.UsageMetadata.PromptTokenCount)
		outputTok = int(resp.UsageMetadata.CandidatesTokenCount)
	}

	return ScoreResult{
		Score:     score,
		Hours:     hours,
		Reasoning: out.Reasoning,
		Usage: RefineUsage{
			InputTokens:  inputTok,
			OutputTokens: outputTok,
			CostMicros:   ComputeCostMicros(s.model, inputTok, outputTok),
			Model:        s.model,
		},
	}, nil
}
