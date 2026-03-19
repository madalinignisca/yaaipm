package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type AccountHandler struct {
	db       *models.DB
	sessions *auth.SessionStore
	engine   *render.Engine
}

func NewAccountHandler(db *models.DB, sessions *auth.SessionStore, engine *render.Engine) *AccountHandler {
	return &AccountHandler{db: db, sessions: sessions, engine: engine}
}

func (h *AccountHandler) AccountSettingsPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)

	h.engine.Render(w, r, "account_settings.html", render.PageData{
		Title:       "Account Settings",
		User:        user,
		Orgs:        middleware.GetOrgs(r),
		Projects:    middleware.GetProjects(r),
		CurrentPath: r.URL.Path,
	})
}

func (h *AccountHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	sess := middleware.GetSession(r)

	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	// Validate
	flashMsg := ""
	if currentPassword == "" || newPassword == "" || confirmPassword == "" {
		flashMsg = "All fields are required"
	} else if len(newPassword) < 12 {
		flashMsg = "New password must be at least 12 characters"
	} else if newPassword != confirmPassword {
		flashMsg = "New passwords do not match"
	}

	if flashMsg == "" {
		// Verify current password
		ok, err := auth.VerifyPassword(currentPassword, user.PasswordHash)
		if err != nil || !ok {
			flashMsg = "Current password is incorrect"
		}
	}

	if flashMsg == "" {
		hash, err := auth.HashPassword(newPassword)
		if err != nil {
			log.Printf("hashing password: %v", err)
			flashMsg = "Failed to update password"
		} else if err := h.db.UpdateUserPassword(r.Context(), user.ID, hash); err != nil {
			log.Printf("updating password: %v", err)
			flashMsg = "Failed to update password"
		} else {
			// Invalidate other sessions for security
			h.sessions.DeleteOtherSessions(r.Context(), user.ID, sess.ID)
			h.renderPage(w, r, user, "Password updated successfully", "success")
			return
		}
	}

	h.renderPage(w, r, user, flashMsg, "error")
}

func (h *AccountHandler) ChangeEmail(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)

	newEmail := strings.TrimSpace(r.FormValue("new_email"))
	password := r.FormValue("password")

	flashMsg := ""
	if newEmail == "" || password == "" {
		flashMsg = "Email and password are required"
	}

	if flashMsg == "" {
		ok, err := auth.VerifyPassword(password, user.PasswordHash)
		if err != nil || !ok {
			flashMsg = "Password is incorrect"
		}
	}

	if flashMsg == "" {
		// Check if email is already taken by another user
		existing, err := h.db.GetUserByEmail(r.Context(), newEmail)
		if err == nil && existing.ID != user.ID {
			flashMsg = "Email is already in use"
		}
	}

	if flashMsg == "" {
		if err := h.db.UpdateUserEmail(r.Context(), user.ID, newEmail); err != nil {
			log.Printf("updating email: %v", err)
			flashMsg = "Failed to update email"
		} else {
			// Reload user to reflect new email
			updated, _ := h.db.GetUserByID(r.Context(), user.ID)
			if updated != nil {
				user = updated
			}
			h.renderPage(w, r, user, "Email updated successfully", "success")
			return
		}
	}

	h.renderPage(w, r, user, flashMsg, "error")
}

func (h *AccountHandler) renderPage(w http.ResponseWriter, r *http.Request, user *models.User, flash, flashType string) {
	h.engine.Render(w, r, "account_settings.html", render.PageData{
		Title:       "Account Settings",
		User:        user,
		Orgs:        middleware.GetOrgs(r),
		Projects:    middleware.GetProjects(r),
		CurrentPath: "/account/settings",
		Flash:       flash,
		FlashType:   flashType,
	})
}
