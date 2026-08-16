package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// scorerOpenAISchema is the strict structured-output schema for the
// {score, hours, reasoning} response.
//
// Deliberately type-only. OpenAI's strict structured outputs accept only
// a subset of JSON Schema and reject numeric constraints (minimum /
// maximum, as with minItems / maxItems), so the 1..10 and >=1 bounds
// CANNOT be expressed here the way Gemini's ResponseSchema expresses
// them. clampScoreFields is the enforcement instead.
//
// The two rules strict mode does impose are both satisfied below:
// additionalProperties must be false, and every property must appear in
// required. Violating either is a 400 from the API, not a local error.
const scorerOpenAISchema = `{
  "type": "object",
  "properties": {
    "score": {"type": "integer"},
    "hours": {"type": "integer"},
    "reasoning": {"type": "string"}
  },
  "required": ["score", "hours", "reasoning"],
  "additionalProperties": false
}`

// scorerOpenAISchemaName labels the schema in the API request; it is not
// part of the response shape.
const scorerOpenAISchemaName = "debate_effort_score"

// OpenAIScorer adapts OpenAI's strict structured outputs to the debate
// Scorer interface. The third scorer of three; selected per project via
// projects.scorer_provider (issue #63).
type OpenAIScorer struct{ c *OpenAIClient }

// NewOpenAIScorer wraps an OpenAIClient as a Scorer. The client is bound
// to a model at construction, so main.go builds a client dedicated to
// SCORER_MODEL_OPENAI rather than reusing the refiner's — scoring runs on
// every accept plus the retry sweep and defaults to a cheaper model.
func NewOpenAIScorer(c *OpenAIClient) *OpenAIScorer { return &OpenAIScorer{c: c} }

func (s *OpenAIScorer) Name() string { return ProviderOpenAI }
func (s *OpenAIScorer) Model() string {
	if s.c == nil {
		return ""
	}
	return s.c.model
}

// buildOpenAIScoreRequest assembles the scoring request for a model.
//
// Split out as a pure function so the reasoning-model branch is testable
// without a network round-trip: go-openai's ReasoningValidator rejects
// MaxTokens on gpt-5*/o1*/o3*/o4* and requires Temperature to be 0 or
// exactly 1, and the default scorer model (gpt-5-mini) IS a reasoning
// model — so getting this wrong breaks every OpenAI-scored project.
// Mirrors OpenAIRefiner.Refine's branch; see isReasoningOpenAIModel.
func buildOpenAIScoreRequest(model, text string) openai.ChatCompletionRequest {
	req := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: debateScoreSystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: text},
		},
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name:   scorerOpenAISchemaName,
				Schema: json.RawMessage(scorerOpenAISchema),
				Strict: true,
			},
		},
	}
	if isReasoningOpenAIModel(model) {
		req.MaxCompletionTokens = scorerMaxTokens
		// Temperature left at zero-value; SDK omits it (default 1).
	} else {
		req.MaxTokens = scorerMaxTokens
		req.Temperature = scorerTemperature
	}
	return req
}

// Score returns {score, hours, reasoning} for the given description.
func (s *OpenAIScorer) Score(ctx context.Context, text string) (ScoreResult, error) {
	if s.c == nil || s.c.client == nil {
		return ScoreResult{}, fmt.Errorf("openai scorer: client not configured")
	}

	resp, err := s.c.client.CreateChatCompletion(ctx, buildOpenAIScoreRequest(s.c.model, text))
	if err != nil {
		return ScoreResult{}, fmt.Errorf("openai scorer: %w", err)
	}
	if len(resp.Choices) == 0 {
		return ScoreResult{}, fmt.Errorf("openai scorer: no choices returned for model %s", s.c.model)
	}

	// A refusal is distinct from an empty completion: the model
	// understood the request and declined it, which is worth its own log
	// line rather than being reported as a malformed response.
	choice := resp.Choices[0]
	if refusal := strings.TrimSpace(choice.Message.Refusal); refusal != "" {
		return ScoreResult{}, fmt.Errorf("openai scorer: model %s refused: %s", s.c.model, refusal)
	}

	raw := strings.TrimSpace(choice.Message.Content)
	if raw == "" {
		return ScoreResult{}, fmt.Errorf("openai scorer: empty response from model %s", s.c.model)
	}

	out, err := parseScorePayload(raw)
	if err != nil {
		return ScoreResult{}, fmt.Errorf("openai scorer: %w", err)
	}

	score, hours := clampScoreFields(out.Score, out.Hours)
	return ScoreResult{
		Score:     score,
		Hours:     hours,
		Reasoning: out.Reasoning,
		Usage: RefineUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			CostMicros:   ComputeCostMicros(s.c.model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
			Model:        s.c.model,
		},
	}, nil
}
