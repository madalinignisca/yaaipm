package mail

import (
	"testing"
)

func TestNewMailer(t *testing.T) {
	m := NewMailer("smtp.example.com", "587", "user@example.com", "secret", "noreply@example.com", false)

	if m.host != "smtp.example.com" {
		t.Errorf("host = %q, want %q", m.host, "smtp.example.com")
	}
	if m.port != "587" {
		t.Errorf("port = %q, want %q", m.port, "587")
	}
	if m.username != "user@example.com" {
		t.Errorf("username = %q, want %q", m.username, "user@example.com")
	}
	if m.password != "secret" {
		t.Errorf("password = %q, want %q", m.password, "secret")
	}
	if m.from != "noreply@example.com" {
		t.Errorf("from = %q, want %q", m.from, "noreply@example.com")
	}
	if m.ssl != false {
		t.Errorf("ssl = %v, want false", m.ssl)
	}
}

func TestNewMailerSSL(t *testing.T) {
	m := NewMailer("smtp.example.com", "465", "user@example.com", "secret", "noreply@example.com", true)

	if m.ssl != true {
		t.Errorf("ssl = %v, want true", m.ssl)
	}

	m2 := NewMailer("smtp.example.com", "587", "user@example.com", "secret", "noreply@example.com", false)

	if m2.ssl != false {
		t.Errorf("ssl = %v, want false", m2.ssl)
	}
}

func TestIsEnabled(t *testing.T) {
	tests := []struct {
		name string
		host string
		from string
		want bool
	}{
		{
			name: "both host and from set",
			host: "smtp.example.com",
			from: "noreply@example.com",
			want: true,
		},
		{
			name: "host empty",
			host: "",
			from: "noreply@example.com",
			want: false,
		},
		{
			name: "from empty",
			host: "smtp.example.com",
			from: "",
			want: false,
		},
		{
			name: "both empty",
			host: "",
			from: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMailer(tt.host, "587", "user", "pass", tt.from, false)
			got := m.IsEnabled()
			if got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSendInvitationDisabled(t *testing.T) {
	m := NewMailer("", "587", "user", "pass", "", false)

	err := m.SendInvitation("recipient@example.com", "Acme Corp", "Alice", "https://example.com/invite/abc123")
	if err != nil {
		t.Errorf("SendInvitation() on disabled mailer returned error: %v", err)
	}
}
