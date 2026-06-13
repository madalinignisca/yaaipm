package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/testutil"
)

// TestCreateRound_ProviderErrorRendersErrorBanner verifies that when a
// refiner fails (e.g. a bad API key or provider outage), the round request
// surfaces a 502 error banner and creates NO round — i.e. the UI shows an
// error rather than a silent empty success.
//
// This covers issue #81's "error-classification" scenario deterministically.
// The real-AI smoke spec drives live providers with valid keys; reproducing
// a bad-key failure there would require booting a second app instance with a
// deliberately-broken key, so the failure path is exercised here instead.
func TestCreateRound_ProviderErrorRendersErrorBanner(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	sessions := auth.NewSessionStore(pool)
	engine, err := render.NewEngine(testutil.ProjectRoot()+"/templates", nil)
	if err != nil {
		t.Fatalf("loading templates: %v", err)
	}

	// A refiner that always fails — simulates an invalid API key / outage.
	refiners := map[string]ai.Refiner{
		"claude": &ai.FakeRefiner{
			NameVal: "claude", ModelVal: ai.ModelClaudeSonnet46,
			OutputFunc: func(_ ai.RefineInput) (string, string, error) {
				return "", "", errors.New("401 unauthorized: invalid api key")
			},
		},
	}
	h := NewDebateHandler(db, engine, refiners, nil, DefaultDebateConfig())

	r := chi.NewRouter()
	r.Use(middleware.Recover)
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(sessions, db))
		r.Post("/tickets/{ticketID}/debate/start", h.StartDebate)
		r.Post("/tickets/{ticketID}/debate/rounds", h.CreateRound)
	})

	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	startReq := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/debate/start", http.NoBody)
	startReq.AddCookie(cookie)
	r.ServeHTTP(httptest.NewRecorder(), startReq)

	form := url.Values{"provider": {"claude"}, "feedback": {"tighten it"}}
	roundReq := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/rounds", strings.NewReader(form.Encode()))
	roundReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	roundReq.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, roundReq)

	// A failed provider must surface a 502 banner — not a 2xx empty success.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "couldn't produce a suggestion") {
		t.Errorf("expected provider-failure banner, got: %q", rec.Body.String())
	}

	// The failure must not leave a phantom round behind.
	deb, err := db.GetActiveDebate(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("GetActiveDebate: %v", err)
	}
	rounds, err := db.GetDebateRounds(context.Background(), deb.ID)
	if err != nil {
		t.Fatalf("GetDebateRounds: %v", err)
	}
	if len(rounds) != 0 {
		t.Errorf("expected 0 rounds after provider failure, got %d", len(rounds))
	}
}
