package ai

import (
	"context"
	"encoding/json"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

// scorerToolName is the forced tool Claude must call to return a score.
//
// Anthropic has no JSON-mode / response_format equivalent, so structured
// output is expressed as a single-tool schema plus tool_choice pinned to
// that tool: the model cannot reply with prose, it can only "call" this
// tool, and the call's arguments ARE the structured payload.
const scorerToolName = "record_effort_score"

// scorerAnthropicProperties is the tool input schema for the
// {score, hours, reasoning} payload.
//
// Type-only, matching the OpenAI schema: Anthropic does not enforce
// numeric bounds in tool input schemas any more than OpenAI strict mode
// does, so clampScoreFields is the enforcement. Declared as a Go map
// rather than a JSON literal because ToolInputSchemaParam.Properties is
// an `any` marshaled by the SDK.
var scorerAnthropicProperties = map[string]any{
	"score":     map[string]any{"type": "integer", "description": "Complexity score from 1 (trivial) to 10 (major project)."},
	"hours":     map[string]any{"type": "integer", "description": "Total human-hours estimate, at least 1."},
	"reasoning": map[string]any{"type": "string", "description": "One sentence naming the biggest risk or scope driver."},
}

// AnthropicScorer adapts Anthropic's forced tool-use to the debate Scorer
// interface. Lives in package ai so it can reach the unexported SDK
// client field on AnthropicClient, same as AnthropicRefiner.
type AnthropicScorer struct {
	client *AnthropicClient
	model  string
}

// NewAnthropicScorer constructs a Scorer over the shared AnthropicClient.
// The model should be one of the ai.Model* constants so the pricing table
// lookup finds a rate; main.go binds it to SCORER_MODEL_CLAUDE.
//
// Note this is the most expensive of the three scorers: the pricing table
// has no cheap Claude tier, so the default is claude-sonnet-4-6 rather
// than a Haiku-class model.
func NewAnthropicScorer(c *AnthropicClient, model string) *AnthropicScorer {
	return &AnthropicScorer{client: c, model: model}
}

func (s *AnthropicScorer) Name() string  { return "claude" }
func (s *AnthropicScorer) Model() string { return s.model }

// buildAnthropicScoreParams assembles the scoring request for a model.
// Split out as a pure function so the forced-tool wiring is testable
// without a network round-trip.
func buildAnthropicScoreParams(model, text string) anthropic.MessageNewParams {
	return anthropic.MessageNewParams{
		Model:       anthropic.Model(model),
		MaxTokens:   scorerMaxTokens,
		Temperature: anthropic.Float(scorerTemperature),
		System: []anthropic.TextBlockParam{
			{Text: debateScoreSystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(text)),
		},
		Tools: []anthropic.ToolUnionParam{{OfTool: &anthropic.ToolParam{
			Name:        scorerToolName,
			Description: anthropic.String("Record the effort score for the feature description."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: scorerAnthropicProperties,
				Required:   []string{"score", "hours", "reasoning"},
			},
		}}},
		// Pin the model to this one tool. DisableParallelToolUse makes it
		// emit exactly one call, so extraction below can take the first
		// matching block without worrying about duplicates.
		ToolChoice: anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{
			Name:                   scorerToolName,
			DisableParallelToolUse: anthropic.Bool(true),
		}},
	}
}

// extractAnthropicToolInput returns the arguments of the first tool_use
// block for the scoring tool.
//
// Pure function over the response so the "model returned prose despite a
// forced tool choice" path is testable without a network call. That case
// should be impossible given the ToolChoice pin above, which is exactly
// why it deserves a distinct error rather than being folded into a parse
// failure — if it ever fires, the forced-tool contract has broken.
//
// The tool name is matched explicitly rather than accepting the first
// tool_use block of any kind: a future second tool on this request must
// not be silently parsed as a score.
func extractAnthropicToolInput(content []anthropic.ContentBlockUnion) (json.RawMessage, error) {
	for _, block := range content {
		if block.Type == "tool_use" && block.Name == scorerToolName {
			return block.Input, nil
		}
	}
	return nil, fmt.Errorf("no %q tool_use block in response", scorerToolName)
}

// Score returns {score, hours, reasoning} for the given description.
func (s *AnthropicScorer) Score(ctx context.Context, text string) (ScoreResult, error) {
	if s.client == nil {
		return ScoreResult{}, fmt.Errorf("anthropic scorer: client not configured")
	}

	resp, err := s.client.client.Messages.New(ctx, buildAnthropicScoreParams(s.model, text))
	if err != nil {
		return ScoreResult{}, fmt.Errorf("anthropic scorer: %w", err)
	}

	input, err := extractAnthropicToolInput(resp.Content)
	if err != nil {
		return ScoreResult{}, fmt.Errorf("anthropic scorer: %w", err)
	}

	out, err := parseScorePayload(string(input))
	if err != nil {
		return ScoreResult{}, fmt.Errorf("anthropic scorer: %w", err)
	}

	inputTok := int(resp.Usage.InputTokens)
	outputTok := int(resp.Usage.OutputTokens)
	score, hours := clampScoreFields(out.Score, out.Hours)

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
