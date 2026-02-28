package ai

import (
	"context"
	"fmt"
	"log"

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
	InputTokens  int32
	OutputTokens int32
	Model        string
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
			Description: "Search tickets in the current project by title or description. Use when the user asks about existing tickets, tasks, bugs, or epics.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"query":  {Type: genai.TypeString, Description: "Search query to match against ticket titles and descriptions"},
					"type":   {Type: genai.TypeString, Description: "Filter by ticket type", Enum: []string{"epic", "task", "subtask", "bug"}},
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
			Description: "Create a new ticket in a project. Ticket hierarchy: epics are top-level features, tasks are children of epics (require parent_id), bugs are top-level. When asked to add tasks to an epic, first search for the epic to get its ID, then create tasks with that epic ID as parent_id.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"project_id":  {Type: genai.TypeString, Description: "UUID of the project"},
					"title":       {Type: genai.TypeString, Description: "Ticket title"},
					"type":        {Type: genai.TypeString, Description: "Ticket type: 'epic' for top-level features, 'task' for work items under an epic, 'subtask' for sub-items under a task, 'bug' for top-level bugs", Enum: []string{"epic", "task", "subtask", "bug"}},
					"priority":    {Type: genai.TypeString, Description: "Ticket priority", Enum: []string{"low", "medium", "high", "critical"}},
					"description": {Type: genai.TypeString, Description: "Detailed description in markdown"},
					"parent_id":   {Type: genai.TypeString, Description: "UUID of the parent ticket. Required for tasks (parent is an epic) and subtasks (parent is a task). Must not be set for epics or bugs."},
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
// onChunk is called with each text chunk from the model.
// Returns token usage data from the final response chunk.
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

	// Split: all but last message as history, last message sent via SendStream
	lastMsg := opts.History[len(opts.History)-1]
	history := opts.History[:len(opts.History)-1]

	chat, err := g.client.Chats.Create(ctx, g.Models.Chat, config, history)
	if err != nil {
		return nil, fmt.Errorf("creating chat: %w", err)
	}

	return g.streamWithToolLoop(ctx, chat, lastMsg.Parts, opts.Executor, onChunk)
}

// streamWithToolLoop handles streaming response and tool call loops.
func (g *GeminiClient) streamWithToolLoop(ctx context.Context, chat *genai.Chat, parts []*genai.Part, executor ToolExecutor, onChunk func(text string)) (*UsageData, error) {
	var lastUsage *UsageData
	for {
		stream := chat.SendStream(ctx, parts...)

		var functionCalls []*genai.FunctionCall
		for chunk, err := range stream {
			if err != nil {
				return lastUsage, fmt.Errorf("streaming: %w", err)
			}

			if chunk == nil || len(chunk.Candidates) == 0 {
				continue
			}

			// Capture cumulative usage from final chunk
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
			}
		}

		// If no function calls, we're done
		if len(functionCalls) == 0 {
			return lastUsage, nil
		}

		// Execute tool calls and send results back
		var responseParts []*genai.Part
		for _, call := range functionCalls {
			log.Printf("AI tool call: %s(%v)", call.Name, call.Args)

			result, err := executor.Execute(ctx, call.Name, call.Args)
			if err != nil {
				result = map[string]any{"error": err.Error()}
			}

			responseParts = append(responseParts, genai.NewPartFromFunctionResponse(call.Name, result))
		}

		// Continue the loop with function responses
		parts = responseParts
	}
}
