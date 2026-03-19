package middleware

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
)

type contextKey string

const (
	SessionContextKey  contextKey = "session"
	UserContextKey     contextKey = "user"
	OrgContextKey      contextKey = "org"
	OrgsContextKey     contextKey = "orgs"
	ProjectsContextKey contextKey = "projects"
)

func GetSession(r *http.Request) *auth.Session {
	sess, _ := r.Context().Value(SessionContextKey).(*auth.Session)
	return sess
}

func GetUser(r *http.Request) *models.User {
	user, _ := r.Context().Value(UserContextKey).(*models.User)
	return user
}

func GetOrg(r *http.Request) *models.Organization {
	org, _ := r.Context().Value(OrgContextKey).(*models.Organization)
	return org
}

func GetOrgs(r *http.Request) []models.Organization {
	orgs, _ := r.Context().Value(OrgsContextKey).([]models.Organization)
	return orgs
}

func GetProjects(r *http.Request) []models.Project {
	projects, _ := r.Context().Value(ProjectsContextKey).([]models.Project)
	return projects
}

var orgSlugRe = regexp.MustCompile(`^/orgs/([^/]+)`)

// extractOrgSlug pulls the org slug from the URL path if present.
func extractOrgSlug(path string) string {
	m := orgSlugRe.FindStringSubmatch(path)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// AuthMiddleware enforces authentication and 2FA on all routes except public ones.
func AuthMiddleware(sessions *auth.SessionStore, db *models.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Public routes
			if path == "/login" || path == "/register" ||
				strings.HasPrefix(path, "/static/") ||
				path == "/setup-2fa" || strings.HasPrefix(path, "/setup-2fa/") ||
				path == "/verify-2fa" || strings.HasPrefix(path, "/verify-2fa/") ||
				strings.HasPrefix(path, "/invite/") ||
				path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			// Get session cookie
			cookie, err := r.Cookie(auth.SessionCookieName)
			if err != nil || cookie.Value == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			sess, err := sessions.GetSession(r.Context(), cookie.Value)
			if err != nil {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			// Logged in but must set up 2FA
			if sess.MustSetup2FA {
				http.Redirect(w, r, "/setup-2fa", http.StatusSeeOther)
				return
			}

			// Logged in but 2FA not verified this session
			if !sess.TwoFactorVerified {
				http.Redirect(w, r, "/verify-2fa", http.StatusSeeOther)
				return
			}

			// Extend session on activity
			_ = sessions.ExtendSession(r.Context(), sess.ID)

			// Load user
			user, err := db.GetUserByID(r.Context(), sess.UserID)
			if err != nil {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			ctx := context.WithValue(r.Context(), SessionContextKey, sess)
			ctx = context.WithValue(ctx, UserContextKey, user)

			// Load org context for sidebar
			var orgs []models.Organization
			if auth.IsStaffOrAbove(user.Role) {
				orgs, _ = db.ListAllOrgs(ctx)
			} else {
				orgs, _ = db.ListUserOrgs(ctx, user.ID)
			}
			ctx = context.WithValue(ctx, OrgsContextKey, orgs)

			// Determine selected org
			var selectedOrg *models.Organization
			if urlSlug := extractOrgSlug(path); urlSlug != "" {
				for i := range orgs {
					if orgs[i].Slug == urlSlug {
						selectedOrg = &orgs[i]
						break
					}
				}
				// Update session if org changed
				if selectedOrg != nil && (sess.SelectedOrgID == nil || *sess.SelectedOrgID != selectedOrg.ID) {
					_ = sessions.SetSelectedOrg(ctx, sess.ID, selectedOrg.ID)
				}
			} else if sess.SelectedOrgID != nil {
				for i := range orgs {
					if orgs[i].ID == *sess.SelectedOrgID {
						selectedOrg = &orgs[i]
						break
					}
				}
			}
			// Fallback: auto-select if exactly one org
			if selectedOrg == nil && len(orgs) == 1 {
				selectedOrg = &orgs[0]
			}

			if selectedOrg != nil {
				ctx = context.WithValue(ctx, OrgContextKey, selectedOrg)
				projects, _ := db.ListProjects(ctx, selectedOrg.ID)
				ctx = context.WithValue(ctx, ProjectsContextKey, projects)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole middleware checks that the user has one of the required roles.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := GetUser(r)
			if user == nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if slices.Contains(roles, user.Role) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "Forbidden", http.StatusForbidden)
		})
	}
}

// Logging middleware logs request details.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(wrapped, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, wrapped.status, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying ResponseWriter if it supports flushing (required for SSE).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter if it supports hijacking (required for WebSocket).
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
}

// Unwrap returns the underlying ResponseWriter for middleware compatibility.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Recover middleware catches panics.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("PANIC: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
