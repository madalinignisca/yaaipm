package ai

import (
	"context"
	"fmt"
	"log"
	"strings"

	"google.golang.org/genai"
)

// GeminiModels holds model identifiers for different use cases.
type GeminiModels struct {
	Default  string // general-purpose model
	Chat     string // chat assistant
	Pro      string // pro model (on-demand)
	Image    string // image generation
	ImagePro string // pro image generation
}

// ToolExecutor executes tool calls from the model and returns results.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (map[string]any, error)
}

// UsageData holds token usage information from a chat response.
type UsageData struct {
	InputTokens    int32
	OutputTokens   int32
	Model          string
	HasImageOutput bool // true when response contains generated images
}

// Thinking level constants for callers that don't import genai directly.
const (
	ThinkingLow  = genai.ThinkingLevelLow
	ThinkingHigh = genai.ThinkingLevelHigh
)

// ChatOpts configures a streaming chat request.
type ChatOpts struct {
	SystemPrompt  string
	History       []*genai.Content
	Executor      ToolExecutor
	ThinkingLevel genai.ThinkingLevel // defaults to LOW if empty
}

// GeminiClient wraps the Gemini API client.
type GeminiClient struct {
	client *genai.Client
	Models GeminiModels
}

// NewGeminiClient creates a new Gemini client with the given API key and model config.
func NewGeminiClient(ctx context.Context, apiKey string, models GeminiModels) (*GeminiClient, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("creating gemini client: %w", err)
	}
	return &GeminiClient{
		client: client,
		Models: models,
	}, nil
}

// toolDeclarations returns the function declarations available to the assistant.
func toolDeclarations() []*genai.FunctionDeclaration {
	return []*genai.FunctionDeclaration{
		{
			Name:        "search_tickets",
			Description: "Search tickets in the current project by title or description. Use when the user asks about existing tickets, tasks, bugs, or features.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"query":  {Type: genai.TypeString, Description: "Search query to match against ticket titles and descriptions"},
					"type":   {Type: genai.TypeString, Description: "Filter by ticket type", Enum: []string{"feature", "task", "subtask", "bug"}},
					"status": {Type: genai.TypeString, Description: "Filter by ticket status", Enum: []string{"backlog", "ready", "planning", "plan_review", "implementing", "testing", "review", "done", "cancelled"}},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "get_project_brief",
			Description: "Get the full project brief markdown. Use when the user asks about the project overview, goals, or specifications.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"project_id": {Type: genai.TypeString, Description: "UUID of the project"},
				},
				Required: []string{"project_id"},
			},
		},
		{
			Name:        "create_ticket",
			Description: "Create a new ticket in a project. Ticket hierarchy: features are top-level, tasks are children of features (require parent_id), bugs are top-level. When asked to add tasks to a feature, first search for the feature to get its ID, then create tasks with that feature ID as parent_id.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"project_id":  {Type: genai.TypeString, Description: "UUID of the project"},
					"title":       {Type: genai.TypeString, Description: "Ticket title"},
					"type":        {Type: genai.TypeString, Description: "Ticket type: 'feature' for top-level features, 'task' for work items under a feature, 'subtask' for sub-items under a task, 'bug' for top-level bugs", Enum: []string{"feature", "task", "subtask", "bug"}},
					"priority":    {Type: genai.TypeString, Description: "Ticket priority", Enum: []string{"low", "medium", "high", "critical"}},
					"description": {Type: genai.TypeString, Description: "Detailed description in markdown"},
					"parent_id":   {Type: genai.TypeString, Description: "UUID of the parent ticket. Required for tasks (parent is a feature) and subtasks (parent is a task). Must not be set for features or bugs."},
				},
				Required: []string{"project_id", "title", "type", "priority"},
			},
		},
		{
			Name:        "update_ticket_status",
			Description: "Update the status of a ticket. Use when the user asks to change a ticket's status.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id":  {Type: genai.TypeString, Description: "UUID of the ticket"},
					"new_status": {Type: genai.TypeString, Description: "New status", Enum: []string{"backlog", "ready", "planning", "plan_review", "implementing", "testing", "review", "done", "cancelled"}},
				},
				Required: []string{"ticket_id", "new_status"},
			},
		},
		{
			Name:        "post_comment",
			Description: "Add a comment to a ticket. Use when the user asks to post a comment on a ticket.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id": {Type: genai.TypeString, Description: "UUID of the ticket"},
					"body":      {Type: genai.TypeString, Description: "Comment body in markdown"},
				},
				Required: []string{"ticket_id", "body"},
			},
		},
		{
			Name:        "update_project_brief",
			Description: "Create or update the project brief (markdown). Use when the user asks to write, update, or replace the project brief content. The brief should be well-structured markdown.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"project_id":     {Type: genai.TypeString, Description: "UUID of the project (optional if conversation is scoped to a project)"},
					"brief_markdown": {Type: genai.TypeString, Description: "The full project brief content in markdown format"},
				},
				Required: []string{"brief_markdown"},
			},
		},
	}
}

// StreamChat sends messages to Gemini with streaming and handles tool calling loops.
// It uses GenerateContentStream directly (not the Chat abstraction) to maintain
// full control over the content array, avoiding issues where the SDK's internal
// history curation drops function call turns when thinking mode produces chunks
// with empty content.
func (g *GeminiClient) StreamChat(ctx context.Context, opts ChatOpts, onChunk func(text string)) (*UsageData, error) {
	thinking := opts.ThinkingLevel
	if thinking == "" {
		thinking = genai.ThinkingLevelLow
	}

	config := &genai.GenerateContentConfig{
		Tools: []*genai.Tool{{
			FunctionDeclarations: toolDeclarations(),
		}},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: opts.SystemPrompt}},
		},
		ThinkingConfig: &genai.ThinkingConfig{
			ThinkingLevel: thinking,
		},
	}

	if len(opts.History) == 0 {
		return nil, fmt.Errorf("no messages in history")
	}

	// We manage the content array ourselves so that function call/response
	// turns are always preserved, regardless of how the SDK validates chunks.
	contents := make([]*genai.Content, len(opts.History))
	copy(contents, opts.History)

	var lastUsage *UsageData
	for {
		stream := g.client.Models.GenerateContentStream(ctx, g.Models.Chat, contents, config)

		var functionCalls []*genai.FunctionCall
		var modelParts []*genai.Part
		for chunk, err := range stream {
			if err != nil {
				return lastUsage, fmt.Errorf("streaming: %w", err)
			}

			if chunk == nil || len(chunk.Candidates) == 0 {
				continue
			}

			if chunk.UsageMetadata != nil {
				lastUsage = &UsageData{
					InputTokens:  chunk.UsageMetadata.PromptTokenCount,
					OutputTokens: chunk.UsageMetadata.CandidatesTokenCount,
					Model:        g.Models.Chat,
				}
			}

			candidate := chunk.Candidates[0]
			if candidate.Content == nil {
				continue
			}

			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					onChunk(part.Text)
				}
				if part.FunctionCall != nil {
					functionCalls = append(functionCalls, part.FunctionCall)
				}
				modelParts = append(modelParts, part)
			}
		}

		// If no function calls, we're done
		if len(functionCalls) == 0 {
			return lastUsage, nil
		}

		// Append the model's response (with function calls) to contents
		contents = append(contents, &genai.Content{
			Role:  "model",
			Parts: modelParts,
		})

		// Execute tool calls and build function response parts
		var responseParts []*genai.Part
		for _, call := range functionCalls {
			log.Printf("AI tool call: %s(%v)", call.Name, call.Args)

			result, err := opts.Executor.Execute(ctx, call.Name, call.Args)
			if err != nil {
				result = map[string]any{"error": err.Error()}
			}

			responseParts = append(responseParts, genai.NewPartFromFunctionResponse(call.Name, result))
		}

		// Append function responses as a user turn, then loop for next model response
		contents = append(contents, &genai.Content{
			Role:  "user",
			Parts: responseParts,
		})
	}
}

// GenerateImage generates an image from a text prompt using the image model.
// Returns the image bytes, MIME type, and usage data.
func (g *GeminiClient) GenerateImage(ctx context.Context, prompt string) ([]byte, string, *UsageData, error) {
	config := &genai.GenerateContentConfig{
		ResponseModalities: []string{"TEXT", "IMAGE"},
	}

	result, err := g.client.Models.GenerateContent(ctx, g.Models.Image,
		[]*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}, config)
	if err != nil {
		return nil, "", nil, fmt.Errorf("generating image: %w", err)
	}

	var usage *UsageData
	if result != nil && result.UsageMetadata != nil {
		usage = &UsageData{
			InputTokens:    result.UsageMetadata.PromptTokenCount,
			OutputTokens:   result.UsageMetadata.CandidatesTokenCount,
			Model:          g.Models.Image,
			HasImageOutput: true,
		}
	}

	if result != nil && len(result.Candidates) > 0 && result.Candidates[0].Content != nil {
		for _, part := range result.Candidates[0].Content.Parts {
			if part.InlineData != nil && len(part.InlineData.Data) > 0 {
				return part.InlineData.Data, part.InlineData.MIMEType, usage, nil
			}
		}
	}

	return nil, "", usage, fmt.Errorf("no image in response")
}

// GenerateTitle generates a short ticket title from a description using the default model.
// Returns the title text and usage data for cost tracking.
func (g *GeminiClient) GenerateTitle(ctx context.Context, ticketType, description string) (string, *UsageData, error) {
	prompt := fmt.Sprintf(
		"Generate a short, clear title (max 10 words) for this %s ticket based on its description. "+
			"Return ONLY the title text, nothing else. No quotes, no prefix, no explanation.\n\nDescription:\n%s",
		ticketType, description,
	)

	result, err := g.client.Models.GenerateContent(ctx, g.Models.Default,
		[]*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}, nil)
	if err != nil {
		return "", nil, fmt.Errorf("generating title: %w", err)
	}

	var usage *UsageData
	if result != nil && result.UsageMetadata != nil {
		usage = &UsageData{
			InputTokens:  result.UsageMetadata.PromptTokenCount,
			OutputTokens: result.UsageMetadata.CandidatesTokenCount,
			Model:        g.Models.Default,
		}
	}

	if result != nil && len(result.Candidates) > 0 && len(result.Candidates[0].Content.Parts) > 0 {
		if text := result.Candidates[0].Content.Parts[0].Text; text != "" {
			return strings.TrimSpace(text), usage, nil
		}
	}

	return "", usage, fmt.Errorf("empty response from model")
}
