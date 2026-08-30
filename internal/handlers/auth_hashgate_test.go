package handlers

import (
	"context"
	"errors"
	"github.com/madalin/forgedesk/internal/auth"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestLoginShedsWhenHashingSaturated covers the handler half of #142.
//
// When every Argon2id slot is taken, login must answer "busy", not "Invalid
// email or password". Collapsing the two would tell a legitimate user their
// credentials are wrong because the server was loaded, and would hide the
// saturation from anyone reading logs or status codes.
func TestLoginShedsWhenHashingSaturated(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "shed@test.com", "client")
	_ = cookie

	form := url.Values{"email": {"shed@test.com"}, "password": {"TestPassword123!"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// A client that has already gone away: the gate must refuse rather than
	// begin a 64 MB computation, and the handler must surface that as 503.
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated login = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Errorf("503 response carries no Retry-After header")
	}
	if strings.Contains(rec.Body.String(), "Invalid email or password") {
		t.Errorf("a busy server told the user their credentials were wrong: %s", rec.Body.String())
	}
}

// TestLoginShedsIdenticallyForUnknownAccount guards the enumeration oracle.
//
// The dummy hash on the unknown-account path exists so timing cannot reveal
// whether an address is registered. If only the known-account path shed while
// the unknown one fell through to "invalid credentials", the difference would
// itself leak the answer.
func TestLoginShedsIdenticallyForUnknownAccount(t *testing.T) {
	r := setupTestRouterOnly(t)

	form := url.Values{"email": {"definitely-not-registered@test.com"}, "password": {"whatever"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated login for unknown account = %d, want 503 (same as a known one)", rec.Code)
	}
}

// TestShedIfHashingBusyMapping tests the error mapping directly.
//
// Found by mutation testing: deleting the busy check from the
// credential-verification branch of Login left every test passing, because a
// cancelled request context makes the database lookup fail first, so both
// end-to-end tests only ever reached the unknown-account branch.
func TestShedIfHashingBusyMapping(t *testing.T) {
	//nolint:dogsled // only the render engine is needed to exercise the mapping
	_, _, _, engine := setupTestRouter(t)
	h := &AuthHandler{engine: engine}

	t.Run("busy sheds with 503 and Retry-After", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)

		if !h.shedIfHashingBusy(rec, req, auth.ErrHashingBusy) {
			t.Fatal("did not handle ErrHashingBusy")
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Error("no Retry-After header")
		}
		if strings.Contains(rec.Body.String(), "Invalid email or password") {
			t.Error("a busy server claimed the credentials were wrong")
		}
	})

	t.Run("other errors are left alone", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)

		if h.shedIfHashingBusy(rec, req, errors.New("some other failure")) {
			t.Error("swallowed an unrelated error as a capacity problem")
		}
		if h.shedIfHashingBusy(rec, req, nil) {
			t.Error("treated a nil error as a capacity problem")
		}
	})
}
