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
