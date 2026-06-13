package handlers

import (
	"net/http"
	"net/http/httptest"
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

// TestShowDebate_NoProvidersConfigured verifies that an active debate
// served by an instance with NO refiners configured (no provider API keys)
// renders the "No AI providers are configured" composer message instead of
// a provider picker — and that the rest of the workspace still renders
// (no partial-render truncation).
//
// This covers issue #81's empty-providers scenario at the render layer,
// where it is deterministic. An e2e version would require booting a second
// app instance with empty provider config (the compose stack runs
// DEBATE_REFINER_MODE=fake, which always injects three fake providers), so
// a render test is the right tool here.
func TestShowDebate_NoProvidersConfigured(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	sessions := auth.NewSessionStore(pool)
	engine, err := render.NewEngine(testutil.ProjectRoot()+"/templates", nil)
	if err != nil {
		t.Fatalf("loading templates: %v", err)
	}

	// Empty refiner registry == no provider API keys configured.
	h := NewDebateHandler(db, engine, map[string]ai.Refiner{}, nil, DefaultDebateConfig())

	r := chi.NewRouter()
	r.Use(middleware.Recover)
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(sessions, db))
		r.Get("/tickets/{ticketID}/debate", h.ShowDebate)
		r.Post("/tickets/{ticketID}/debate/start", h.StartDebate)
	})

	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	// Start a debate so ShowDebate renders the composer partial.
	startRec := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/debate/start", http.NoBody)
	startReq.AddCookie(cookie)
	r.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusSeeOther {
		t.Fatalf("start debate status = %d, want 303; body = %q", startRec.Code, startRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID+"/debate", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "No AI providers are configured") {
		t.Errorf("expected empty-providers message in composer; not found")
	}
	// No provider radio picker when the refiner registry is empty.
	if strings.Contains(body, `name="provider"`) {
		t.Errorf("provider picker rendered despite empty refiner registry")
	}
	// The rest of the workspace still renders — guards against a partial
	// template that truncates when the provider list is empty.
	for _, marker := range []string{
		`data-testid="debate-composer"`,
		`data-testid="debate-approve"`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("missing marker %q — partial-render regression?", marker)
		}
	}
}
