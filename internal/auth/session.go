package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SessionCookieName = "forgedesk_session"
	SessionLifetime   = 24 * time.Hour
)

type Session struct {
	ExpiresAt         time.Time
	SelectedOrgID     *string
	ID                string
	UserID            string
	IPAddress         string
	UserAgent         string
	TwoFactorVerified bool
	MustSetup2FA      bool
}

type SessionStore struct {
	db *pgxpool.Pool
}

func NewSessionStore(db *pgxpool.Pool) *SessionStore {
	return &SessionStore{db: db}
}

// CreateSession creates a new session and returns the raw token for the cookie.
func (s *SessionStore) CreateSession(ctx context.Context, userID string, mustSetup2FA bool, r *http.Request) (string, error) {
	tokenBytes := make([]byte, 32) // 256 bits
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	rawToken := hex.EncodeToString(tokenBytes)
	tokenHash := hashToken(rawToken)

	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = fwd
	}

	_, err := s.db.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, must_setup_2fa, ip_address, user_agent, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		tokenHash, userID, mustSetup2FA, ip, r.UserAgent(), time.Now().Add(SessionLifetime),
	)
	if err != nil {
		return "", fmt.Errorf("inserting session: %w", err)
	}

	return rawToken, nil
}

// GetSession retrieves a session by raw token.
func (s *SessionStore) GetSession(ctx context.Context, rawToken string) (*Session, error) {
	tokenHash := hashToken(rawToken)
	sess := &Session{}
	err := s.db.QueryRow(ctx,
		`SELECT id, user_id, two_factor_verified, must_setup_2fa, ip_address, user_agent, selected_org_id, expires_at
		 FROM sessions WHERE token_hash = $1 AND expires_at > now()`,
		tokenHash,
	).Scan(&sess.ID, &sess.UserID, &sess.TwoFactorVerified, &sess.MustSetup2FA, &sess.IPAddress, &sess.UserAgent, &sess.SelectedOrgID, &sess.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// MarkTwoFactorVerified sets two_factor_verified to true.
func (s *SessionStore) MarkTwoFactorVerified(ctx context.Context, sessionID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE sessions SET two_factor_verified = TRUE WHERE id = $1`, sessionID)
	return err
}

// Mark2FASetupComplete clears the must_setup_2fa flag.
func (s *SessionStore) Mark2FASetupComplete(ctx context.Context, sessionID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE sessions SET must_setup_2fa = FALSE WHERE id = $1`, sessionID)
	return err
}

// SetSelectedOrg updates the selected org for a session.
func (s *SessionStore) SetSelectedOrg(ctx context.Context, sessionID, orgID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE sessions SET selected_org_id = $1 WHERE id = $2`, orgID, sessionID)
	return err
}

// ExtendSession slides the expiration forward.
func (s *SessionStore) ExtendSession(ctx context.Context, sessionID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE sessions SET expires_at = $1 WHERE id = $2`,
		time.Now().Add(SessionLifetime), sessionID)
	return err
}

// DeleteSession removes a single session.
func (s *SessionStore) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID)
	return err
}

// DeleteAllUserSessions removes all sessions for a user.
func (s *SessionStore) DeleteAllUserSessions(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

// DeleteOtherSessions removes all sessions except the given one.
func (s *SessionStore) DeleteOtherSessions(ctx context.Context, userID, keepSessionID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND id != $2`, userID, keepSessionID)
	return err
}

// CleanExpired removes expired sessions.
func (s *SessionStore) CleanExpired(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
