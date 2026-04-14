package ai

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicModels holds model identifiers for different use cases.
type AnthropicModels struct {
	Default string // general-purpose model (e.g. sonnet for planning)
	Content string // high-quality model (e.g. opus for implementation)
}

// AnthropicClient wraps the Anthropic Messages API.
type AnthropicClient struct {
	Models AnthropicModels
	client anthropic.Client
}

// NewAnthropicClient creates a new Anthropic client with the given API key and models.
func NewAnthropicClient(apiKey string, models AnthropicModels) *AnthropicClient {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &AnthropicClient{
		client: client,
		Models: models,
	}
}

// GenerateResponse sends a single-turn request and returns the text response.
func (a *AnthropicClient) GenerateResponse(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, *UsageData, error) {
	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)),
		},
	})
	if err != nil {
		return "", nil, fmt.Errorf("anthropic messages.new: %w", err)
	}

	var textParts []string
	for _, block := range resp.Content {
		if block.Type == "text" {
			textParts = append(textParts, block.Text)
		}
	}

	text := strings.Join(textParts, "\n")
	if text == "" {
		return "", nil, fmt.Errorf("empty response from anthropic model %s", model)
	}

	usage := &UsageData{
		InputTokens:  int32(resp.Usage.InputTokens),
		OutputTokens: int32(resp.Usage.OutputTokens),
		Model:        model,
	}

	return text, usage, nil
}

// ── Feature Debate Mode (Task 4) ──────────────────────────────────

// AnthropicRefiner adapts the Anthropic Messages API to the debate
// Refiner interface. Lives in this file so it can reach the unexported
// SDK client field on AnthropicClient without widening the public API.
type AnthropicRefiner struct {
	client *AnthropicClient
	model  string
}

// NewAnthropicRefiner constructs a Refiner over the shared AnthropicClient.
// The model string should be a member of ai.Model* constants (e.g.
// ModelClaudeSonnet46) so the pricing table lookup finds a rate.
func NewAnthropicRefiner(c *AnthropicClient, model string) *AnthropicRefiner {
	return &AnthropicRefiner{client: c, model: model}
}

func (r *AnthropicRefiner) Name() string  { return "claude" }
func (r *AnthropicRefiner) Model() string { return r.model }

// Refine sends one refactoring request and returns the model's output
// along with normalized usage and finish-reason data. SystemPrompt is
// taken from the input if set; otherwise the caller hasn't loaded the
// embedded prompt (the debate handler is responsible for that).
func (r *AnthropicRefiner) Refine(ctx context.Context, in RefineInput) (RefineOutput, error) {
	if r.client == nil {
		return RefineOutput{}, fmt.Errorf("anthropic refiner: client not configured")
	}

	resp, err := r.client.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(r.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: in.SystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(
				buildRefineUserPrompt(in.CurrentText, in.Feedback),
			)),
		},
	})
	if err != nil {
		return RefineOutput{}, fmt.Errorf("anthropic refine: %w", err)
	}

	var textParts []string
	for _, block := range resp.Content {
		if block.Type == "text" {
			textParts = append(textParts, block.Text)
		}
	}
	text := strings.Join(textParts, "\n")
	// Adapter-level empty-response guard. The CreateRound handler also
	// rejects empty/short output via MinOutputLen (spec §3.2), so this
	// is defense in depth — if the handler check is ever weakened, the
	// adapter still refuses to return {Text: "", FinishReason: "stop"}
	// that could silently overwrite a ticket description on approve.
	if text == "" {
		return RefineOutput{}, fmt.Errorf("anthropic refine: empty response from model %s", r.model)
	}

	inputTok := int(resp.Usage.InputTokens)
	outputTok := int(resp.Usage.OutputTokens)

	return RefineOutput{
		Text:         text,
		FinishReason: mapAnthropicStopReason(resp.StopReason),
		Usage: RefineUsage{
			InputTokens:  inputTok,
			OutputTokens: outputTok,
			CostMicros:   ComputeCostMicros(r.model, inputTok, outputTok),
			Model:        r.model,
		},
	}, nil
}

// mapAnthropicStopReason normalizes Anthropic's stop_reason vocabulary
// to the Refiner FinishReason contract (see refiner.go). Unknown values
// are surfaced raw so the handler can treat them as "stop-equivalent".
func mapAnthropicStopReason(reason anthropic.StopReason) string {
	switch reason {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
		return FinishReasonStop
	case anthropic.StopReasonMaxTokens:
		return FinishReasonLength
	case anthropic.StopReasonToolUse:
		return FinishReasonToolCalls
	case anthropic.StopReasonRefusal:
		return FinishReasonContentFilter
	default:
		return string(reason)
	}
}
