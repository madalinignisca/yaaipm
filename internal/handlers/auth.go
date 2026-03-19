package handlers

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type AuthHandler struct {
	db           *models.DB
	sessions     *auth.SessionStore
	engine       *render.Engine
	aesKey       string
	secureCookie bool
}

func NewAuthHandler(db *models.DB, sessions *auth.SessionStore, engine *render.Engine, aesKey string, secureCookie bool) *AuthHandler {
	return &AuthHandler{db: db, sessions: sessions, engine: engine, aesKey: aesKey, secureCookie: secureCookie}
}

func (h *AuthHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	_ = h.engine.Render(w, r, "login.html", render.PageData{Title: "Login"})
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	if email == "" || password == "" {
		_ = h.engine.Render(w, r, "login.html", render.PageData{
			Title: "Login", Flash: "Email and password are required.", FlashType: "error",
		})
		return
	}

	user, err := h.db.GetUserByEmail(r.Context(), email)
	if err != nil {
		// Hash the password anyway to equalize timing (prevents user enumeration)
		_, _ = auth.HashPassword(password)
		_ = h.engine.Render(w, r, "login.html", render.PageData{
			Title: "Login", Flash: "Invalid email or password.", FlashType: "error",
		})
		return
	}

	ok, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		_ = h.engine.Render(w, r, "login.html", render.PageData{
			Title: "Login", Flash: "Invalid email or password.", FlashType: "error",
		})
		return
	}

	token, err := h.sessions.CreateSession(r.Context(), user.ID, user.MustSetup2FA, r)
	if err != nil {
		log.Printf("creating session: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})

	if user.MustSetup2FA {
		http.Redirect(w, r, "/setup-2fa", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/verify-2fa", http.StatusSeeOther)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	sess := middleware.GetSession(r)
	if sess != nil {
		_ = h.sessions.DeleteSession(r.Context(), sess.ID)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *AuthHandler) RegisterPage(w http.ResponseWriter, r *http.Request) {
	count, err := h.db.CountUsers(r.Context())
	if err != nil || count > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	_ = h.engine.Render(w, r, "register.html", render.PageData{Title: "Register"})
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	count, err := h.db.CountUsers(r.Context())
	if err != nil || count > 0 {
		http.Error(w, "Registration closed", http.StatusForbidden)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	name := strings.TrimSpace(r.FormValue("name"))

	if email == "" || password == "" || name == "" {
		_ = h.engine.Render(w, r, "register.html", render.PageData{
			Title: "Register", Flash: "All fields are required.", FlashType: "error",
		})
		return
	}

	if len(password) < 12 {
		_ = h.engine.Render(w, r, "register.html", render.PageData{
			Title: "Register", Flash: "Password must be at least 12 characters.", FlashType: "error",
		})
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Printf("hashing password: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// First user becomes superadmin (only path here since count == 0)
	role := auth.RoleSuperAdmin

	_, err = h.db.CreateUser(r.Context(), email, hash, name, role)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			_ = h.engine.Render(w, r, "register.html", render.PageData{
				Title: "Register", Flash: "Email already registered.", FlashType: "error",
			})
			return
		}
		log.Printf("creating user: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	_ = h.engine.Render(w, r, "login.html", render.PageData{
		Title: "Login", Flash: "Account created. Please log in.", FlashType: "success",
	})
}

func (h *AuthHandler) Setup2FAPage(w http.ResponseWriter, r *http.Request) {
	_ = h.engine.Render(w, r, "setup_2fa.html", render.PageData{Title: "Set Up Two-Factor Authentication"})
}

func (h *AuthHandler) Setup2FATOTP(w http.ResponseWriter, r *http.Request) {
	sess := h.getSessionFromCookie(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	user, err := h.db.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	secret, qrBase64, err := auth.GenerateTOTP(user.Email)
	if err != nil {
		log.Printf("generating TOTP: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Store secret temporarily encrypted
	encSecret, err := auth.EncryptTOTPSecret(secret, h.aesKey)
	if err != nil {
		log.Printf("encrypting TOTP secret: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Save unverified for now
	if err := h.db.UpdateUserTOTP(r.Context(), user.ID, encSecret, false, "totp"); err != nil {
		log.Printf("saving TOTP: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	_ = h.engine.Render(w, r, "setup_2fa_totp.html", render.PageData{
		Title: "Set Up Authenticator App",
		Data: map[string]string{
			"QRCode":    qrBase64,
			"ManualKey": secret,
		},
	})
}

func (h *AuthHandler) VerifySetupTOTP(w http.ResponseWriter, r *http.Request) {
	sess := h.getSessionFromCookie(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		_ = h.engine.Render(w, r, "setup_2fa_totp.html", render.PageData{
			Title: "Set Up Authenticator App", Flash: "Please enter the 6-digit code.", FlashType: "error",
		})
		return
	}

	user, err := h.db.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	secret, err := auth.DecryptTOTPSecret(user.TOTPSecret, h.aesKey)
	if err != nil {
		log.Printf("decrypting TOTP: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if !auth.ValidateTOTP(code, secret) {
		_ = h.engine.Render(w, r, "setup_2fa_totp.html", render.PageData{
			Title: "Set Up Authenticator App", Flash: "Invalid code. Please try again.", FlashType: "error",
			Data: map[string]string{"ManualKey": secret},
		})
		return
	}

	// Mark TOTP as verified
	encSecret, _ := auth.EncryptTOTPSecret(secret, h.aesKey)
	if updateErr := h.db.UpdateUserTOTP(r.Context(), user.ID, encSecret, true, "totp"); updateErr != nil {
		log.Printf("updating TOTP verified: %v", updateErr)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Generate recovery codes
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		log.Printf("generating recovery codes: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	hashedCodes, err := auth.HashRecoveryCodes(codes)
	if err != nil {
		log.Printf("hashing recovery codes: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	encCodes, err := auth.EncryptRecoveryCodes(hashedCodes, h.aesKey)
	if err != nil {
		log.Printf("encrypting recovery codes: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if err := h.db.UpdateUserRecoveryCodes(r.Context(), user.ID, encCodes); err != nil {
		log.Printf("saving recovery codes: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Mark session as fully authenticated
	_ = h.sessions.Mark2FASetupComplete(r.Context(), sess.ID)
	_ = h.sessions.MarkTwoFactorVerified(r.Context(), sess.ID)

	_ = h.engine.Render(w, r, "recovery_codes.html", render.PageData{
		Title: "Recovery Codes",
		Data:  codes,
	})
}

func (h *AuthHandler) Verify2FAPage(w http.ResponseWriter, r *http.Request) {
	sess := h.getSessionFromCookie(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	user, err := h.db.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	method := "totp"
	if user.Preferred2FAMethod != nil {
		method = *user.Preferred2FAMethod
	}

	_ = h.engine.Render(w, r, "verify_2fa.html", render.PageData{
		Title: "Two-Factor Verification",
		Data:  map[string]string{"Method": method},
	})
}

func (h *AuthHandler) Verify2FA(w http.ResponseWriter, r *http.Request) {
	sess := h.getSessionFromCookie(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		_ = h.engine.Render(w, r, "verify_2fa.html", render.PageData{
			Title: "Two-Factor Verification", Flash: "Please enter the code.", FlashType: "error",
			Data: map[string]string{"Method": "totp"},
		})
		return
	}

	user, err := h.db.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Try TOTP first (with replay prevention)
	if user.TOTPVerified && user.TOTPSecret != nil {
		secret, err := auth.DecryptTOTPSecret(user.TOTPSecret, h.aesKey)
		if err == nil {
			lastUsed, _ := h.db.GetTOTPLastUsed(r.Context(), user.ID)
			if auth.ValidateTOTPOnce(code, secret, lastUsed) {
				_ = h.db.UpdateTOTPLastUsed(r.Context(), user.ID, time.Now())
				_ = h.sessions.MarkTwoFactorVerified(r.Context(), sess.ID)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
	}

	// Try recovery code
	if user.RecoveryCodes != nil {
		hashedCodes, err := auth.DecryptRecoveryCodes(user.RecoveryCodes, h.aesKey)
		if err == nil {
			idx := auth.VerifyRecoveryCode(code, hashedCodes)
			if idx >= 0 {
				hashedCodes[idx] = "" // consume
				encCodes, err := auth.EncryptRecoveryCodes(hashedCodes, h.aesKey)
				if err == nil {
					_ = h.db.ConsumeRecoveryCode(r.Context(), user.ID, encCodes)
				}
				_ = h.sessions.MarkTwoFactorVerified(r.Context(), sess.ID)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
	}

	_ = h.engine.Render(w, r, "verify_2fa.html", render.PageData{
		Title: "Two-Factor Verification", Flash: "Invalid code.", FlashType: "error",
		Data: map[string]string{"Method": "totp"},
	})
}

func (h *AuthHandler) getSessionFromCookie(r *http.Request) *auth.Session {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	sess, err := h.sessions.GetSession(r.Context(), cookie.Value)
	if err != nil {
		return nil
	}
	return sess
}
