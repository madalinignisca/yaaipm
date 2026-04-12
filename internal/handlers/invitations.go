package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/mail"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

const statusPending = "pending"

type InviteHandler struct {
	db           *models.DB
	sessions     *auth.SessionStore
	engine       *render.Engine
	mailer       *mail.Mailer
	aesKey       string
	baseURL      string
	secureCookie bool
}

func NewInviteHandler(db *models.DB, sessions *auth.SessionStore, engine *render.Engine, mailer *mail.Mailer, aesKey, baseURL string, secureCookie bool) *InviteHandler {
	return &InviteHandler{
		db:           db,
		sessions:     sessions,
		engine:       engine,
		mailer:       mailer,
		aesKey:       aesKey,
		baseURL:      baseURL,
		secureCookie: secureCookie,
	}
}

func hashInviteToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func generateInviteToken() (raw, hash string, err error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(tokenBytes)
	hash = hashInviteToken(raw)
	return raw, hash, nil
}

// InviteRegisterPage renders the registration form for an invite link.
func (h *InviteHandler) InviteRegisterPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	tokenHash := hashInviteToken(token)

	inv, err := h.db.GetInvitationByToken(r.Context(), tokenHash)
	if err != nil {
		_ = h.engine.Render(w, r, "invite_register.html", render.PageData{
			Title: "Invalid Invitation",
			Flash: "This invitation link is invalid or has expired.", FlashType: "error",
		})
		return
	}

	// If user already exists, redirect to login
	if _, err := h.db.GetUserByEmail(r.Context(), inv.Email); err == nil {
		_ = h.engine.Render(w, r, "login.html", render.PageData{
			Title: "Login",
			Flash: "You already have an account. Please log in to accept the invitation.", FlashType: "success",
		})
		return
	}

	org, _ := h.db.GetOrgByID(r.Context(), inv.OrgID)
	inviter, _ := h.db.GetUserByID(r.Context(), inv.InvitedBy)

	orgName := ""
	inviterName := ""
	if org != nil {
		orgName = org.Name
	}
	if inviter != nil {
		inviterName = inviter.Name
	}

	_ = h.engine.Render(w, r, "invite_register.html", render.PageData{
		Title: "Join " + orgName,
		Data: map[string]any{
			"Email":       inv.Email,
			"OrgName":     orgName,
			"InviterName": inviterName,
			"Token":       token,
		},
	})
}

// InviteRegister handles the registration form submission from an invite link.
func (h *InviteHandler) InviteRegister(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	tokenHash := hashInviteToken(token)

	inv, err := h.db.GetInvitationByToken(r.Context(), tokenHash)
	if err != nil {
		_ = h.engine.Render(w, r, "invite_register.html", render.PageData{
			Title: "Invalid Invitation",
			Flash: "This invitation link is invalid or has expired.", FlashType: "error",
		})
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	password := r.FormValue("password")

	org, _ := h.db.GetOrgByID(r.Context(), inv.OrgID)
	inviter, _ := h.db.GetUserByID(r.Context(), inv.InvitedBy)
	orgName := ""
	inviterName := ""
	if org != nil {
		orgName = org.Name
	}
	if inviter != nil {
		inviterName = inviter.Name
	}

	renderErr := func(msg string) {
		_ = h.engine.Render(w, r, "invite_register.html", render.PageData{
			Title: "Join " + orgName, Flash: msg, FlashType: "error",
			Data: map[string]any{
				"Email":       inv.Email,
				"OrgName":     orgName,
				"InviterName": inviterName,
				"Token":       token,
			},
		})
	}

	if name == "" || password == "" {
		renderErr("All fields are required.")
		return
	}

	if len(password) < 12 {
		renderErr("Password must be at least 12 characters.")
		return
	}

	// Use email from invitation, not from form
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Printf("hashing password: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Create user, mark invitation accepted, and add org membership
	// atomically so a transient failure on any downstream write cannot
	// leave a user account with no org membership and a half-consumed
	// invitation. The session is created only after Commit succeeds. (#28)
	user, err := h.db.AcceptInviteTx(r.Context(), inv.Email, hash, name, auth.RoleClient, inv.ID, inv.OrgID, inv.OrgRole)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			renderErr("An account with this email already exists. Please log in.")
			return
		}
		log.Printf("accepting invite: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Create session
	sessionToken, err := h.sessions.CreateSession(r.Context(), user.ID, true, r)
	if err != nil {
		log.Printf("creating session: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})

	http.Redirect(w, r, "/setup-2fa", http.StatusSeeOther)
}

// AcceptInvitation accepts a pending invitation for an authenticated user.
func (h *InviteHandler) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	invitationID := r.PathValue("invitationID")

	inv, err := h.db.GetInvitationByID(r.Context(), invitationID)
	if err != nil {
		http.Error(w, "Invitation not found", http.StatusNotFound)
		return
	}

	if !strings.EqualFold(inv.Email, user.Email) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if inv.Status != statusPending || inv.ExpiresAt.Before(time.Now()) {
		http.Error(w, "Invitation is no longer valid", http.StatusGone)
		return
	}

	// #31: AddOrgMember is an upsert that would overwrite role on conflict,
	// so it is not safe to use when we want to refuse role changes. Use an
	// atomic INSERT ... ON CONFLICT DO NOTHING instead. If the row was not
	// inserted, the user was already a member (via another path — direct
	// admin add, prior accept, etc.) and we deliberately leave the
	// existing role untouched. Any real datastore error is propagated as
	// 500 so operational incidents are not masked as success. The
	// returned `inserted` flag is informational only — it doesn't affect
	// the response (see the status-reconciliation note below).
	// (#31 — review feedback)
	if _, err := h.db.InsertOrgMembershipIfAbsent(r.Context(), user.ID, inv.OrgID, inv.OrgRole); err != nil {
		log.Printf("inserting org member on accept: %v", err)
		http.Error(w, "Failed to join organization", http.StatusInternalServerError)
		return
	}

	// Mark the invitation accepted regardless of whether the insert was a
	// no-op. Two reasons: (1) self-healing retry — if a previous accept
	// inserted the membership but UpdateInvitationStatus failed (DB
	// blip), the retry still reconciles the pending record; (2) the
	// invitation's purpose ("make this user a member") is already
	// satisfied when the user is in the org via any path, so a
	// remaining "pending" state serves no purpose and clutters the
	// dashboard. Log but do not fail on a status-update error — the
	// membership is what matters for access.
	// (#31 — review feedback on 4ef2e0b)
	if statusErr := h.db.UpdateInvitationStatus(r.Context(), inv.ID, "accepted"); statusErr != nil {
		log.Printf("reconciling invitation status: %v", statusErr)
	}

	// Return 200 with an empty body. hx-swap="outerHTML" on the dashboard
	// relies on a 2xx response to remove the invite card from the DOM; a
	// 409 here would leave a stale card that the user can re-click,
	// triggering a 410 Gone the second time and a confusing "broken app"
	// experience until a full page refresh. Both the "just inserted" and
	// "already a member, status reconciled" cases are logically success
	// from the user's perspective — the invitation is consumed and the
	// user is in the org. (#31 — review feedback from Codex on 487541c)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(""))
}

// DeclineInvitation declines a pending invitation.
func (h *InviteHandler) DeclineInvitation(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	invitationID := r.PathValue("invitationID")

	inv, err := h.db.GetInvitationByID(r.Context(), invitationID)
	if err != nil {
		http.Error(w, "Invitation not found", http.StatusNotFound)
		return
	}

	if !strings.EqualFold(inv.Email, user.Email) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if inv.Status != statusPending {
		http.Error(w, "Invitation is no longer valid", http.StatusGone)
		return
	}

	if err := h.db.UpdateInvitationStatus(r.Context(), inv.ID, "declined"); err != nil {
		log.Printf("declining invitation: %v", err)
		http.Error(w, "Failed to decline", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(""))
}

// RevokeInvitation revokes a pending invitation (org admin action).
func (h *InviteHandler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	orgSlug := r.PathValue("orgSlug")
	invitationID := r.PathValue("invitationID")

	org, err := h.db.GetOrgBySlug(r.Context(), orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if !canManageOrg(h.db, r, user, org.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	inv, err := h.db.GetInvitationByID(r.Context(), invitationID)
	if err != nil || inv.OrgID != org.ID {
		http.Error(w, "Invitation not found", http.StatusNotFound)
		return
	}

	if err := h.db.UpdateInvitationStatus(r.Context(), inv.ID, "expired"); err != nil {
		log.Printf("revoking invitation: %v", err)
		http.Error(w, "Failed to revoke", http.StatusInternalServerError)
		return
	}

	// Re-render invitation list
	invitations, _ := h.db.ListOrgInvitations(r.Context(), org.ID)
	if err := h.engine.RenderPartial(w, "invitation_list.html", map[string]any{
		"Invitations": invitations,
		"OrgSlug":     org.Slug,
		"CanManage":   true,
	}); err != nil {
		log.Printf("rendering invitation list partial: %v", err)
	}
}

// ResendInvitation generates a new token and resends the invitation email.
func (h *InviteHandler) ResendInvitation(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	orgSlug := r.PathValue("orgSlug")
	invitationID := r.PathValue("invitationID")

	org, err := h.db.GetOrgBySlug(r.Context(), orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if !canManageOrg(h.db, r, user, org.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	inv, err := h.db.GetInvitationByID(r.Context(), invitationID)
	if err != nil || inv.OrgID != org.ID {
		http.Error(w, "Invitation not found", http.StatusNotFound)
		return
	}

	if inv.Status != statusPending {
		http.Error(w, "Only pending invitations can be resent", http.StatusBadRequest)
		return
	}

	rawToken, tokenHash, err := generateInviteToken()
	if err != nil {
		log.Printf("generating invite token: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	newExpiry := time.Now().Add(7 * 24 * time.Hour)
	if err := h.db.ResetInvitationToken(r.Context(), inv.ID, tokenHash, newExpiry); err != nil {
		log.Printf("resetting invitation token: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	inviteURL := h.baseURL + "/invite/" + rawToken
	if err := h.mailer.SendInvitation(inv.Email, org.Name, user.Name, inviteURL); err != nil {
		log.Printf("resending invitation email: %v", err)
	}

	// Re-render invitation list
	invitations, _ := h.db.ListOrgInvitations(r.Context(), org.ID)
	if err := h.engine.RenderPartial(w, "invitation_list.html", map[string]any{
		"Invitations": invitations,
		"OrgSlug":     org.Slug,
		"CanManage":   true,
	}); err != nil {
		log.Printf("rendering invitation list partial: %v", err)
	}
}

// canManageOrg is a shared helper to check org management permission.
func canManageOrg(db *models.DB, r *http.Request, user *models.User, orgID string) bool {
	if auth.IsStaffOrAbove(user.Role) {
		return true
	}
	m, err := db.GetOrgMembership(r.Context(), user.ID, orgID)
	if err != nil {
		return false
	}
	return auth.CanManageOrg(m.Role)
}
