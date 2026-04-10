package middleware

import (
	"net/http"

	csrf "filippo.io/csrf/gorilla"
)

// CSRFProtect returns CSRF middleware configured for ForgeDesk.
// authKey should be a 32-byte key (the session secret works well).
//
// The filippo.io/csrf/gorilla package is a drop-in replacement for the
// archived gorilla/csrf. Its option functions (Secure, Path, SameSite,
// HttpOnly) are kept for source compatibility but are no-ops because
// the new implementation uses header-based CSRF protection
// (Origin / Sec-Fetch-Site) and does not issue its own cookies.
// We keep the option call sites for symmetry with the original config
// and to make future re-introduction of a cookie-based scheme trivial;
// the staticcheck deprecation is acknowledged and suppressed below.
func CSRFProtect(authKey []byte, secure bool) func(http.Handler) http.Handler {
	return csrf.Protect(
		authKey,
		csrf.Secure(secure),                    //nolint:staticcheck // filippo.io/csrf/gorilla shim — no-op, kept for symmetry
		csrf.Path("/"),                         //nolint:staticcheck // filippo.io/csrf/gorilla shim — no-op, kept for symmetry
		csrf.SameSite(csrf.SameSiteStrictMode), //nolint:staticcheck // filippo.io/csrf/gorilla shim — no-op, kept for symmetry
		csrf.HttpOnly(true),                    //nolint:staticcheck // filippo.io/csrf/gorilla shim — no-op, kept for symmetry
		csrf.ErrorHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Forbidden - invalid CSRF token", http.StatusForbidden)
		})),
	)
}
