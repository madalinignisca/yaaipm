package models

import "testing"

// TestCommentAuthorName pins the three cases the ticket page and the HTMX
// partial both route through (#95). The deleted-user case is here rather
// than in a handler test because deleting a user with comments is awkward
// to stage through HTTP, and it is the case most likely to regress: it is
// the only one that depends on a NULL surviving the LEFT JOIN.
func TestCommentAuthorName(t *testing.T) {
	str := func(s string) *string { return &s }

	tests := []struct {
		name      string
		agentName *string
		userName  *string
		want      string
	}{
		{"human comment shows the real name", nil, str("Alice Author"), "Alice Author"},
		{"agent comment is never named to the client", str("claude"), nil, "ForgeDesk Bot"},
		{"agent wins even if a user row is somehow joined", str("gemini"), str("Alice"), "ForgeDesk Bot"},
		{"deleted author falls back rather than showing a blank byline", nil, nil, "User"},
		{"empty name is treated as no name", nil, str(""), "User"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CommentAuthorName(tt.agentName, tt.userName); got != tt.want {
				t.Errorf("CommentAuthorName() = %q, want %q", got, tt.want)
			}
		})
	}
}
