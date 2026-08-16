package ai

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/sashabaranov/go-openai"
)

// Compile-time interface assertions for every Scorer implementation.
// Signature drift fails the build before any test runs.
var (
	_ Scorer = (*GeminiScorer)(nil)
	_ Scorer = (*OpenAIScorer)(nil)
	_ Scorer = (*AnthropicScorer)(nil)
)

func TestClampScoreFields(t *testing.T) {
	cases := []struct {
		name                 string
		score, hours         int
		wantScore, wantHours int
	}{
		{"in range passes through", 7, 12, 7, 12},
		{"lower bounds", 1, 1, 1, 1},
		{"upper score bound", 10, 900, 10, 900},
		{"score above range clamps to 10", 47, 5, 10, 5},
		{"score below range clamps to 1", 0, 5, 1, 5},
		{"negative score clamps to 1", -3, 5, 1, 5},
		{"zero hours clamps to 1", 5, 0, 5, 1},
		{"negative hours clamps to 1", 5, -8, 5, 1},
		{"both out of range", 99, 0, 10, 1},
		// A model returning nothing parses to the zero value; clamping
		// keeps that off the effort chip as 1/1 rather than 0/0.
		{"zero value", 0, 0, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotScore, gotHours := clampScoreFields(c.score, c.hours)
			if gotScore != c.wantScore || gotHours != c.wantHours {
				t.Errorf("clampScoreFields(%d, %d) = (%d, %d), want (%d, %d)",
					c.score, c.hours, gotScore, gotHours, c.wantScore, c.wantHours)
			}
		})
	}
}

// The startup pricing guard (issue #108/#109) iterates the scorer registry
// calling Model(), so every adapter must report the model it will actually
// bill against — including the nil-client case, which must not panic.
func TestScorer_NameAndModel(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		s := NewGeminiScorer(nil, ModelGeminiFlash)
		if s.Name() != "gemini" {
			t.Errorf("Name() = %q, want gemini", s.Name())
		}
		if s.Model() != ModelGeminiFlash {
			t.Errorf("Model() = %q, want %s", s.Model(), ModelGeminiFlash)
		}
	})

	t.Run("openai", func(t *testing.T) {
		s := NewOpenAIScorer(NewOpenAIClient("fake-key", ModelGPT5Mini))
		if s.Name() != "openai" {
			t.Errorf("Name() = %q, want openai", s.Name())
		}
		if s.Model() != ModelGPT5Mini {
			t.Errorf("Model() = %q, want %s", s.Model(), ModelGPT5Mini)
		}
	})

	t.Run("openai model empty when client nil", func(t *testing.T) {
		s := &OpenAIScorer{c: nil}
		if s.Model() != "" {
			t.Errorf("Model() with nil client = %q, want empty", s.Model())
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		s := NewAnthropicScorer(nil, ModelClaudeSonnet46)
		if s.Name() != "claude" {
			t.Errorf("Name() = %q, want claude", s.Name())
		}
		if s.Model() != ModelClaudeSonnet46 {
			t.Errorf("Model() = %q, want %s", s.Model(), ModelClaudeSonnet46)
		}
	})

	t.Run("fake", func(t *testing.T) {
		f := &FakeScorer{NameVal: "gemini", ModelVal: ModelGeminiFlash}
		if f.Name() != "gemini" {
			t.Errorf("Name() = %q, want gemini", f.Name())
		}
		if f.Model() != ModelGeminiFlash {
			t.Errorf("Model() = %q, want %s", f.Model(), ModelGeminiFlash)
		}
	})
}

// The default OpenAI scorer model (gpt-5-mini) is a reasoning model, and
// go-openai's ReasoningValidator rejects MaxTokens / a non-0-or-1
// Temperature for those CLIENT-SIDE. Getting this branch wrong breaks
// every OpenAI-scored project, so assert both shapes directly.
func TestBuildOpenAIScoreRequest_ModelBranch(t *testing.T) {
	t.Run("reasoning model uses MaxCompletionTokens and omits Temperature", func(t *testing.T) {
		req := buildOpenAIScoreRequest(ModelGPT5Mini, "some feature")
		if req.MaxCompletionTokens != scorerMaxTokens {
			t.Errorf("MaxCompletionTokens = %d, want %d", req.MaxCompletionTokens, scorerMaxTokens)
		}
		if req.MaxTokens != 0 {
			t.Errorf("MaxTokens = %d, want 0 for a reasoning model", req.MaxTokens)
		}
		if req.Temperature != 0 {
			t.Errorf("Temperature = %v, want unset for a reasoning model", req.Temperature)
		}
	})

	t.Run("legacy model uses MaxTokens and a low Temperature", func(t *testing.T) {
		req := buildOpenAIScoreRequest("gpt-4o-mini", "some feature")
		if req.MaxTokens != scorerMaxTokens {
			t.Errorf("MaxTokens = %d, want %d", req.MaxTokens, scorerMaxTokens)
		}
		if req.MaxCompletionTokens != 0 {
			t.Errorf("MaxCompletionTokens = %d, want 0 for a legacy model", req.MaxCompletionTokens)
		}
		if req.Temperature != scorerTemperature {
			t.Errorf("Temperature = %v, want %v", req.Temperature, scorerTemperature)
		}
	})
}

func TestBuildOpenAIScoreRequest_StructuredOutput(t *testing.T) {
	req := buildOpenAIScoreRequest(ModelGPT5Mini, "some feature")

	if req.Model != ModelGPT5Mini {
		t.Errorf("Model = %q, want %q", req.Model, ModelGPT5Mini)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2 (system + user)", len(req.Messages))
	}
	if req.Messages[0].Content != debateScoreSystemPrompt {
		t.Error("first message should carry the embedded scorer system prompt")
	}
	if req.Messages[1].Content != "some feature" {
		t.Errorf("user message = %q, want the text under test", req.Messages[1].Content)
	}
	if req.ResponseFormat == nil {
		t.Fatal("ResponseFormat must be set — without it the model may return prose")
	}
	if req.ResponseFormat.Type != openai.ChatCompletionResponseFormatTypeJSONSchema {
		t.Errorf("ResponseFormat.Type = %q, want json_schema", req.ResponseFormat.Type)
	}
	if req.ResponseFormat.JSONSchema == nil {
		t.Fatal("JSONSchema must be set")
	}
	if !req.ResponseFormat.JSONSchema.Strict {
		t.Error("Strict must be true — otherwise the schema is advisory only")
	}
	if req.ResponseFormat.JSONSchema.Name != scorerOpenAISchemaName {
		t.Errorf("schema Name = %q, want %q", req.ResponseFormat.JSONSchema.Name, scorerOpenAISchemaName)
	}
}

// OpenAI rejects a strict schema that omits additionalProperties:false or
// leaves any property out of required — with a 400 at call time, which no
// unit test of ours would otherwise catch. Pin both invariants here.
func TestScorerOpenAISchema_StrictModeInvariants(t *testing.T) {
	var schema struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal([]byte(scorerOpenAISchema), &schema); err != nil {
		t.Fatalf("scorerOpenAISchema is not valid JSON: %v", err)
	}

	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Error("strict mode requires additionalProperties:false")
	}
	if len(schema.Required) != len(schema.Properties) {
		t.Errorf("strict mode requires every property in required: %d properties, %d required",
			len(schema.Properties), len(schema.Required))
	}
	for _, field := range []string{"score", "hours", "reasoning"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("schema missing property %q", field)
		}
		if !slices.Contains(schema.Required, field) {
			t.Errorf("schema missing %q in required", field)
		}
	}
}

func TestParseScorePayload(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		got, err := parseScorePayload(`{"score":7,"hours":12,"reasoning":"Touches auth."}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Score != 7 || got.Hours != 12 || got.Reasoning != "Touches auth." {
			t.Errorf("parsed = %+v, want {7 12 Touches auth.}", got)
		}
	})

	t.Run("prose instead of JSON errors", func(t *testing.T) {
		_, err := parseScorePayload("I think this is about a 7.")
		if err == nil {
			t.Fatal("expected a parse error")
		}
		if !strings.Contains(err.Error(), "I think this is about a 7.") {
			t.Errorf("error should include the raw text for diagnosis, got: %v", err)
		}
	})

	// A runaway response must not dump kilobytes into the logs.
	t.Run("long raw text is truncated in the error", func(t *testing.T) {
		_, err := parseScorePayload(strings.Repeat("x", 5000))
		if err == nil {
			t.Fatal("expected a parse error")
		}
		if len(err.Error()) > 400 {
			t.Errorf("error message is %d chars, want it truncated", len(err.Error()))
		}
	})
}

func TestBuildAnthropicScoreParams(t *testing.T) {
	params := buildAnthropicScoreParams(ModelClaudeSonnet46, "some feature")

	if len(params.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want exactly 1 (the forced scoring tool)", len(params.Tools))
	}
	tool := params.Tools[0].OfTool
	if tool == nil {
		t.Fatal("Tools[0].OfTool must be set")
	}
	if tool.Name != scorerToolName {
		t.Errorf("tool Name = %q, want %q", tool.Name, scorerToolName)
	}

	// Without tool_choice pinned to our tool, Claude may answer in prose
	// and there is no structured output at all.
	if params.ToolChoice.OfTool == nil {
		t.Fatal("ToolChoice must pin the scoring tool")
	}
	if params.ToolChoice.OfTool.Name != scorerToolName {
		t.Errorf("ToolChoice name = %q, want %q", params.ToolChoice.OfTool.Name, scorerToolName)
	}
	if len(tool.InputSchema.Required) != 3 {
		t.Errorf("InputSchema.Required = %v, want all three fields", tool.InputSchema.Required)
	}
	if params.MaxTokens != scorerMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", params.MaxTokens, scorerMaxTokens)
	}
}

func TestExtractAnthropicToolInput(t *testing.T) {
	t.Run("returns the matching tool_use input", func(t *testing.T) {
		content := []anthropic.ContentBlockUnion{
			{Type: "text", Text: "Let me score that."},
			{Type: "tool_use", Name: scorerToolName, Input: json.RawMessage(`{"score":4}`)},
		}
		got, err := extractAnthropicToolInput(content)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != `{"score":4}` {
			t.Errorf("input = %s, want {\"score\":4}", got)
		}
	})

	// Should be impossible given the forced ToolChoice — which is exactly
	// why it gets its own error rather than surfacing as a parse failure.
	t.Run("prose-only response errors", func(t *testing.T) {
		content := []anthropic.ContentBlockUnion{{Type: "text", Text: "About a 7, I'd say."}}
		if _, err := extractAnthropicToolInput(content); err == nil {
			t.Fatal("expected an error when no tool_use block is present")
		}
	})

	t.Run("ignores a tool_use block for a different tool", func(t *testing.T) {
		content := []anthropic.ContentBlockUnion{
			{Type: "tool_use", Name: "some_other_tool", Input: json.RawMessage(`{"score":9}`)},
		}
		if _, err := extractAnthropicToolInput(content); err == nil {
			t.Fatal("expected an error when only a different tool was called")
		}
	})
}

func TestScorer_ScoreWithoutClientErrors(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		s := &OpenAIScorer{c: nil}
		if _, err := s.Score(t.Context(), "text"); err == nil {
			t.Fatal("expected error when client is nil")
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		s := &AnthropicScorer{client: nil, model: ModelClaudeSonnet46}
		if _, err := s.Score(t.Context(), "text"); err == nil {
			t.Fatal("expected error when client is nil")
		}
	})
}
