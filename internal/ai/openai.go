package ai

import (
	"context"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// OpenAIClient is a thin wrapper around the sashabaranov/go-openai SDK.
// Shape mirrors AnthropicClient so future non-debate uses can plug in
// without architectural churn.
type OpenAIClient struct {
	client *openai.Client
	model  string
}

// NewOpenAIClient constructs a client bound to a specific model. The
// model is stored on the wrapper so callers don't need to pass it to
// every request; model-per-request callers can simply build multiple
// clients.
func NewOpenAIClient(apiKey, model string) *OpenAIClient {
	return &OpenAIClient{
		client: openai.NewClient(apiKey),
		model:  model,
	}
}

// OpenAIRefiner adapts OpenAIClient to the debate Refiner interface.
// The third of three providers backing the debate's AI-picker buttons.
type OpenAIRefiner struct{ c *OpenAIClient }

// NewOpenAIRefiner wraps an OpenAIClient as a Refiner. The wrapper
// exists so the Name() returns "openai" regardless of which specific
// model is configured — the handler uses Name() for the button label
// and Model() for the audit trail.
func NewOpenAIRefiner(c *OpenAIClient) *OpenAIRefiner { return &OpenAIRefiner{c: c} }

func (r *OpenAIRefiner) Name() string { return "openai" }
func (r *OpenAIRefiner) Model() string {
	if r.c == nil {
		return ""
	}
	return r.c.model
}

// Refine sends one ChatCompletion request and returns the normalized
// RefineOutput. Same adapter-layer guards as the other two providers:
// empty/whitespace-only output is rejected here so a silent "" can't
// slip past even if the handler's MinOutputLen check regresses.
func (r *OpenAIRefiner) Refine(ctx context.Context, in RefineInput) (RefineOutput, error) {
	if r.c == nil || r.c.client == nil {
		return RefineOutput{}, fmt.Errorf("openai refiner: client not configured")
	}

	resp, err := r.c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: r.c.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: resolveSystemPrompt(in.SystemPrompt)},
			{Role: openai.ChatMessageRoleUser, Content: buildRefineUserPrompt(in.CurrentText, in.Feedback)},
		},
		MaxTokens:   refinerMaxTokens,
		Temperature: refinerTemperature,
	})
	if err != nil {
		return RefineOutput{}, fmt.Errorf("openai refine: %w", err)
	}
	if len(resp.Choices) == 0 {
		return RefineOutput{}, fmt.Errorf("openai refine: no choices returned for model %s", r.c.model)
	}

	choice := resp.Choices[0]
	text := strings.TrimSpace(choice.Message.Content)
	if text == "" {
		return RefineOutput{}, fmt.Errorf("openai refine: empty response from model %s", r.c.model)
	}

	return RefineOutput{
		Text:         text,
		FinishReason: mapOpenAIFinishReason(choice.FinishReason),
		Usage: RefineUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			CostMicros:   ComputeCostMicros(r.c.model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
			Model:        r.c.model,
		},
	}, nil
}

// mapOpenAIFinishReason normalizes the OpenAI SDK's FinishReason to
// the Refiner contract. OpenAI's vocabulary already overlaps heavily
// with ours (the identity mapping covers "stop", "length", "content_
// filter", "tool_calls"), so this is mostly a typed pass-through.
// function_call is merged into tool_calls since the debate UI treats
// them identically.
func mapOpenAIFinishReason(reason openai.FinishReason) string {
	switch reason {
	case openai.FinishReasonStop:
		return FinishReasonStop
	case openai.FinishReasonLength:
		return FinishReasonLength
	case openai.FinishReasonContentFilter:
		return FinishReasonContentFilter
	case openai.FinishReasonToolCalls, openai.FinishReasonFunctionCall:
		return FinishReasonToolCalls
	case openai.FinishReasonNull, "":
		// "null" / empty happens mid-stream and in edge cases; treat as
		// stop-equivalent since our handler only acts on explicit
		// truncation/filter signals.
		return FinishReasonStop
	default:
		// Tightened unknown-reason heuristic: classify into ContentFilter
		// before Length so a future "safety_limit_exceeded" doesn't get
		// mis-recorded as a truncation in the audit trail. Truncation
		// patterns must contain a token/length/truncat marker — the
		// standalone "exceeded" substring is too broad and was removed.
		raw := strings.ToLower(string(reason))
		switch {
		case strings.Contains(raw, "safety") ||
			strings.Contains(raw, "refus") ||
			strings.Contains(raw, "filter") ||
			strings.Contains(raw, "prohibit") ||
			strings.Contains(raw, "block"):
			return FinishReasonContentFilter
		case strings.Contains(raw, "max_tokens") ||
			strings.Contains(raw, "max_completion") ||
			strings.Contains(raw, "context_length") ||
			strings.Contains(raw, "length") ||
			strings.Contains(raw, "truncat"):
			return FinishReasonLength
		default:
			return string(reason)
		}
	}
}
