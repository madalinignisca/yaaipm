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
	Model          string
	InputTokens    int32
	OutputTokens   int32
	HasImageOutput bool
}

// Thinking level constants for callers that don't import genai directly.
const (
	ThinkingLow  = genai.ThinkingLevelLow
	ThinkingHigh = genai.ThinkingLevelHigh
)

// ChatOpts configures a streaming chat request.
type ChatOpts struct {
	Executor      ToolExecutor
	SystemPrompt  string
	ThinkingLevel genai.ThinkingLevel
	History       []*genai.Content
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

// ticketStatusEnum lists the status values declared in the DB CHECK
// constraint on tickets.status (migrations/000006_create_tickets.up.sql).
// Centralized here so drift between the three tool declarations
// below cannot reintroduce the cancelled/canceled bug (#35). The
// British "cancelled" spelling is intentional — see .golangci.yml.
var ticketStatusEnum = []string{
	"backlog", "ready", "planning", "plan_review",
	"implementing", "testing", "review", "done", "cancelled",
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
					"status": {Type: genai.TypeString, Description: "Filter by ticket status", Enum: ticketStatusEnum},
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
					"date_start":  {Type: genai.TypeString, Description: "Start date in YYYY-MM-DD format (e.g. 2026-03-15). Used for timeline/Gantt view."},
					"date_end":    {Type: genai.TypeString, Description: "End date in YYYY-MM-DD format (e.g. 2026-03-20). Used for timeline/Gantt view."},
				},
				Required: []string{"project_id", "type", "priority"},
			},
		},
		{
			Name:        "update_ticket_status",
			Description: "Update the status of a ticket. Use when the user asks to change a ticket's status.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id":  {Type: genai.TypeString, Description: "UUID of the ticket"},
					"new_status": {Type: genai.TypeString, Description: "New status", Enum: ticketStatusEnum},
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
			Name:        "update_ticket",
			Description: "Update fields on an existing ticket (title, description, priority, dates). Use when the user asks to edit or modify a ticket's details. Only provided fields will be updated. To change a ticket's status, use update_ticket_status instead.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id":   {Type: genai.TypeString, Description: "UUID of the ticket to update"},
					"title":       {Type: genai.TypeString, Description: "New title (optional)"},
					"description": {Type: genai.TypeString, Description: "New description in markdown (optional)"},
					"priority":    {Type: genai.TypeString, Description: "New priority (optional)", Enum: []string{"low", "medium", "high", "critical"}},
					"date_start":  {Type: genai.TypeString, Description: "Start date in YYYY-MM-DD format (optional)"},
					"date_end":    {Type: genai.TypeString, Description: "End date in YYYY-MM-DD format (optional)"},
				},
				Required: []string{"ticket_id"},
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
		{
			Name:        "get_ticket",
			Description: "Get full details of a specific ticket including description, comments, and child tickets. Use when the user asks about a specific ticket or you need to see its full content.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id": {Type: genai.TypeString, Description: "UUID of the ticket"},
				},
				Required: []string{"ticket_id"},
			},
		},
		{
			Name:        "list_tickets",
			Description: "List tickets by type or by parent. Use to get all features in a project, all bugs, or all children (tasks/subtasks) of a specific ticket. More efficient than search when you want a complete list.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"project_id": {Type: genai.TypeString, Description: "UUID of the project (optional if conversation is scoped to a project)"},
					"type":       {Type: genai.TypeString, Description: "Filter by ticket type", Enum: []string{"feature", "task", "subtask", "bug"}},
					"parent_id":  {Type: genai.TypeString, Description: "List children of this ticket (overrides type filter)"},
				},
			},
		},
		{
			Name:        "archive_ticket",
			Description: "Archive a ticket (soft-delete). Staff/superadmin only. Use when a ticket is no longer needed but should be preserved for reference.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id": {Type: genai.TypeString, Description: "UUID of the ticket to archive"},
				},
				Required: []string{"ticket_id"},
			},
		},
		{
			Name:        "restore_ticket",
			Description: "Restore an archived ticket. Staff/superadmin only.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id": {Type: genai.TypeString, Description: "UUID of the ticket to restore"},
				},
				Required: []string{"ticket_id"},
			},
		},
		{
			Name:        "delete_ticket",
			Description: "Permanently delete a ticket. Staff/superadmin only. This action is irreversible — confirm with the user first.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id": {Type: genai.TypeString, Description: "UUID of the ticket to permanently delete"},
				},
				Required: []string{"ticket_id"},
			},
		},
		{
			Name:        "update_agent_mode",
			Description: "Set the agent mode and agent name on a ticket for orchestrator dispatch. Staff/superadmin only.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"ticket_id":  {Type: genai.TypeString, Description: "UUID of the ticket"},
					"agent_mode": {Type: genai.TypeString, Description: "Agent mode. Use 'none' to clear.", Enum: []string{"plan", "implement", "none"}},
					"agent_name": {Type: genai.TypeString, Description: "Agent to assign. Use 'none' to clear.", Enum: []string{"claude", "gemini", "codex", "mistral", "none"}},
				},
				Required: []string{"ticket_id"},
			},
		},
		{
			Name:        "update_repo_url",
			Description: "Set or update the repository URL for a project. Staff/superadmin only.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"project_id": {Type: genai.TypeString, Description: "UUID of the project"},
					"repo_url":   {Type: genai.TypeString, Description: "Repository URL (e.g. https://github.com/org/repo)"},
				},
				Required: []string{"project_id", "repo_url"},
			},
		},
		{
			Name:        "mark_brief_reviewed",
			Description: "Mark the project brief as reviewed. Staff/superadmin only. Creates a revision record indicating the brief was reviewed.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"project_id": {Type: genai.TypeString, Description: "UUID of the project (optional if conversation is scoped to a project)"},
				},
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
	contents := make([]*genai.Content, 0, len(opts.History)+1)
	contents = append(contents, opts.History...)

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
				// Only keep parts that carry actual data — skip empty
				// thinking-mode marker parts that have no text/call/data,
				// as sending them back to the API triggers INVALID_ARGUMENT.
				if part.Text != "" || part.FunctionCall != nil || part.FunctionResponse != nil || part.InlineData != nil {
					modelParts = append(modelParts, part)
				}
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
func (g *GeminiClient) GenerateImage(ctx context.Context, prompt string) (imgBytes []byte, mimeType string, usage *UsageData, err error) {
	config := &genai.GenerateContentConfig{
		ResponseModalities: []string{"TEXT", "IMAGE"},
	}

	result, err := g.client.Models.GenerateContent(ctx, g.Models.Image,
		[]*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}, config)
	if err != nil {
		return nil, "", nil, fmt.Errorf("generating image: %w", err)
	}

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
