package middleware

import (
	"net/http"

	"github.com/gorilla/csrf"
)

// CSRFProtect returns CSRF middleware configured for ForgeDesk.
// authKey should be a 32-byte key (the session secret works well).
func CSRFProtect(authKey []byte, secure bool) func(http.Handler) http.Handler {
	return csrf.Protect(
		authKey,
		csrf.Secure(secure),
		csrf.Path("/"),
		csrf.SameSite(csrf.SameSiteStrictMode),
		csrf.HttpOnly(true),
		csrf.ErrorHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Forbidden - invalid CSRF token", http.StatusForbidden)
		})),
	)
}
