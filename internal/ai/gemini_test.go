package ai

import (
	"testing"

	"google.golang.org/genai"
)

func TestToolDeclarations_ReturnsExpectedTools(t *testing.T) {
	decls := toolDeclarations()

	expected := []string{
		"search_tickets",
		"get_project_brief",
		"create_ticket",
		"update_ticket_status",
		"post_comment",
		"update_project_brief",
	}

	if len(decls) != len(expected) {
		t.Fatalf("expected %d tool declarations, got %d", len(expected), len(decls))
	}

	names := make(map[string]bool, len(decls))
	for _, d := range decls {
		names[d.Name] = true
	}

	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing expected tool declaration: %s", name)
		}
	}
}

func TestToolDeclarations_AllHaveNameDescriptionAndObjectParams(t *testing.T) {
	for _, d := range toolDeclarations() {
		t.Run(d.Name, func(t *testing.T) {
			if d.Name == "" {
				t.Error("Name is empty")
			}
			if d.Description == "" {
				t.Errorf("Description is empty for %s", d.Name)
			}
			if d.Parameters == nil {
				t.Fatalf("Parameters is nil for %s", d.Name)
			}
			if d.Parameters.Type != genai.TypeObject {
				t.Errorf("expected Parameters.Type == TypeObject for %s, got %v", d.Name, d.Parameters.Type)
			}
		})
	}
}

func TestToolDeclarations_CreateTicketRequiredFields(t *testing.T) {
	var createTicket *genai.FunctionDeclaration
	for _, d := range toolDeclarations() {
		if d.Name == "create_ticket" {
			createTicket = d
			break
		}
	}
	if createTicket == nil {
		t.Fatal("create_ticket declaration not found")
	}

	required := map[string]bool{
		"project_id": false,
		"title":      false,
		"type":       false,
		"priority":   false,
	}

	for _, r := range createTicket.Parameters.Required {
		if _, ok := required[r]; ok {
			required[r] = true
		}
	}

	for field, found := range required {
		if !found {
			t.Errorf("create_ticket missing required field: %s", field)
		}
	}
}

func TestToolDeclarations_SearchTicketsRequiredFields(t *testing.T) {
	var searchTickets *genai.FunctionDeclaration
	for _, d := range toolDeclarations() {
		if d.Name == "search_tickets" {
			searchTickets = d
			break
		}
	}
	if searchTickets == nil {
		t.Fatal("search_tickets declaration not found")
	}

	if len(searchTickets.Parameters.Required) != 1 || searchTickets.Parameters.Required[0] != "query" {
		t.Errorf("expected search_tickets to require [\"query\"], got %v", searchTickets.Parameters.Required)
	}
}

func TestGeminiModels_FieldsStoredCorrectly(t *testing.T) {
	m := GeminiModels{
		Default:  "gemini-2.5-flash",
		Chat:     "gemini-2.5-flash",
		Pro:      "gemini-2.5-pro",
		Image:    "imagen-3.0-generate-002",
		ImagePro: "imagen-3.0-generate-001",
	}

	if m.Default != "gemini-2.5-flash" {
		t.Errorf("Default = %q, want %q", m.Default, "gemini-2.5-flash")
	}
	if m.Chat != "gemini-2.5-flash" {
		t.Errorf("Chat = %q, want %q", m.Chat, "gemini-2.5-flash")
	}
	if m.Pro != "gemini-2.5-pro" {
		t.Errorf("Pro = %q, want %q", m.Pro, "gemini-2.5-pro")
	}
	if m.Image != "imagen-3.0-generate-002" {
		t.Errorf("Image = %q, want %q", m.Image, "imagen-3.0-generate-002")
	}
	if m.ImagePro != "imagen-3.0-generate-001" {
		t.Errorf("ImagePro = %q, want %q", m.ImagePro, "imagen-3.0-generate-001")
	}
}

func TestUsageData_FieldsStoredCorrectly(t *testing.T) {
	u := UsageData{
		InputTokens:  150,
		OutputTokens: 300,
		Model:        "gemini-2.5-flash",
	}

	if u.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want 150", u.InputTokens)
	}
	if u.OutputTokens != 300 {
		t.Errorf("OutputTokens = %d, want 300", u.OutputTokens)
	}
	if u.Model != "gemini-2.5-flash" {
		t.Errorf("Model = %q, want %q", u.Model, "gemini-2.5-flash")
	}
}

func TestThinkingConstants(t *testing.T) {
	if ThinkingLow != genai.ThinkingLevelLow {
		t.Errorf("ThinkingLow = %v, want genai.ThinkingLevelLow (%v)", ThinkingLow, genai.ThinkingLevelLow)
	}
	if ThinkingHigh != genai.ThinkingLevelHigh {
		t.Errorf("ThinkingHigh = %v, want genai.ThinkingLevelHigh (%v)", ThinkingHigh, genai.ThinkingLevelHigh)
	}
	if ThinkingLow == ThinkingHigh {
		t.Error("ThinkingLow and ThinkingHigh should be different values")
	}
}
