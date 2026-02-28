package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/testutil"
)

func TestAuthMiddlewarePublicRoutes(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	sessions := auth.NewSessionStore(pool)
	db := models.NewDB(pool)

	handler := AuthMiddleware(sessions, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	publicPaths := []string{"/login", "/register", "/static/css/app.css", "/setup-2fa", "/verify-2fa", "/health"}

	for _, path := range publicPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("public route %s returned %d, want 200", path, rec.Code)
			}
		})
	}
}

func TestAuthMiddlewareRedirectsUnauthenticated(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	sessions := auth.NewSessionStore(pool)
	db := models.NewDB(pool)

	handler := AuthMiddleware(sessions, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %s", loc)
	}
}

func TestAuthMiddlewareRedirectsToSetup2FA(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	sessions := auth.NewSessionStore(pool)
	db := models.NewDB(pool)

	ctx := context.Background()
	var u1ID string
	pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, name, role) VALUES ('mw@test.com', 'hash', 'MW', 'client') RETURNING id`).Scan(&u1ID)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	token, _ := sessions.CreateSession(ctx, u1ID, true, req)

	handler := AuthMiddleware(sessions, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req2)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/setup-2fa" {
		t.Errorf("expected redirect to /setup-2fa, got %s", loc)
	}
}

func TestAuthMiddlewareRedirectsToVerify2FA(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	sessions := auth.NewSessionStore(pool)
	db := models.NewDB(pool)

	ctx := context.Background()
	var u2ID string
	err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, name, role, must_setup_2fa) VALUES ('verify@test.com', 'hash', 'V', 'client', false) RETURNING id`).Scan(&u2ID)
	if err != nil {
		t.Fatalf("inserting user: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	token, err := sessions.CreateSession(ctx, u2ID, false, req)
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}

	handler := AuthMiddleware(sessions, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req2)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/verify-2fa" {
		t.Errorf("expected redirect to /verify-2fa, got %s", loc)
	}
}

func TestAuthMiddlewarePassesFullyAuthenticated(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	sessions := auth.NewSessionStore(pool)
	db := models.NewDB(pool)

	ctx := context.Background()
	var u3ID string
	pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, name, role, must_setup_2fa) VALUES ('full@test.com', 'hash', 'Full', 'client', false) RETURNING id`).Scan(&u3ID)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	token, _ := sessions.CreateSession(ctx, u3ID, false, req)
	sess, _ := sessions.GetSession(ctx, token)
	sessions.MarkTwoFactorVerified(ctx, sess.ID)

	var capturedUser *models.User
	handler := AuthMiddleware(sessions, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = GetUser(r)
		w.WriteHeader(http.StatusOK)
	}))

	req2 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req2.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req2)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if capturedUser == nil {
		t.Fatal("user should be in context")
	}
	if capturedUser.Email != "full@test.com" {
		t.Errorf("email = %q, want full@test.com", capturedUser.Email)
	}
}

func TestRequireRoleAllowed(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RequireRole("superadmin")(inner)

	user := &models.User{Role: "superadmin"}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	ctx := context.WithValue(req.Context(), UserContextKey, user)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequireRoleForbidden(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RequireRole("superadmin")(inner)

	user := &models.User{Role: "client"}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	ctx := context.WithValue(req.Context(), UserContextKey, user)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestRequireRoleNoUser(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RequireRole("superadmin")(inner)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestLoggingMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	handler := Logging(inner)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", rec.Code)
	}
}

func TestRecoverMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	handler := Recover(inner)
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}
