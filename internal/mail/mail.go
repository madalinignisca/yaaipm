package mail

import (
	"crypto/tls"
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
	ssl      bool
}

func NewMailer(host, port, username, password, from string, ssl bool) *Mailer {
	return &Mailer{
		host:     host,
		port:     port,
		username: username,
		password: password,
		from:     from,
		ssl:      ssl,
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

	if m.ssl {
		if err := m.sendSSL(addr, auth, to, []byte(msg)); err != nil {
			log.Printf("sending invitation email to %s: %v", to, err)
			return err
		}
		return nil
	}

	if err := smtp.SendMail(addr, auth, m.from, []string{to}, []byte(msg)); err != nil {
		log.Printf("sending invitation email to %s: %v", to, err)
		return err
	}
	return nil
}

// sendSSL connects via implicit TLS (SMTPS, port 465).
func (m *Mailer) sendSSL(addr string, auth smtp.Auth, to string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: m.host})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(m.from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}

	return client.Quit()
}
