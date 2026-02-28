package mail

import (
	"fmt"
	"log"
	"net/smtp"
	"strings"
)

type Mailer struct {
	host     string
	port     string
	username string
	password string
	from     string
}

func NewMailer(host, port, username, password, from string) *Mailer {
	return &Mailer{
		host:     host,
		port:     port,
		username: username,
		password: password,
		from:     from,
	}
}

func (m *Mailer) IsEnabled() bool {
	return m.host != "" && m.from != ""
}

func (m *Mailer) SendInvitation(to, orgName, inviterName, inviteURL string) error {
	if !m.IsEnabled() {
		return nil
	}

	subject := fmt.Sprintf("You've been invited to join %s on ForgeDesk", orgName)
	body := fmt.Sprintf(`%s has invited you to join the organization "%s" on ForgeDesk.

Click the link below to accept the invitation:

%s

This invitation will expire in 7 days.

If you did not expect this invitation, you can safely ignore this email.
`, inviterName, orgName, inviteURL)

	msg := strings.Join([]string{
		"From: " + m.from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")

	addr := m.host + ":" + m.port
	auth := smtp.PlainAuth("", m.username, m.password, m.host)

	if err := smtp.SendMail(addr, auth, m.from, []string{to}, []byte(msg)); err != nil {
		log.Printf("sending invitation email to %s: %v", to, err)
		return err
	}
	return nil
}
