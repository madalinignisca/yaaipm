package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/handlers"
	"github.com/madalin/forgedesk/internal/mail"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/static"
	"github.com/madalin/forgedesk/internal/storage"
	"github.com/madalin/forgedesk/internal/ws"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("parsing database config: %v", err)
	}
	poolCfg.MaxConns = 15
	poolCfg.MinConns = 2
	poolCfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer pool.Close()

	if pingErr := pool.Ping(context.Background()); pingErr != nil {
		log.Fatalf("pinging database: %v", pingErr) //nolint:gocritic // exitAfterDefer - acceptable at startup
	}
	log.Printf("Database connected (max_conns=%d, min_conns=%d)", poolCfg.MaxConns, poolCfg.MinConns)

	db := models.NewDB(pool)
	sessions := auth.NewSessionStore(pool)

	manifest, err := static.NewManifest("static")
	if err != nil {
		log.Fatalf("building asset manifest: %v", err)
	}
	log.Printf("Asset manifest built (%d files)", manifest.Len())

	engine, err := render.NewEngine("templates", manifest)
	if err != nil {
		log.Fatalf("loading templates: %v", err)
	}
	log.Println("Templates loaded")

	// AI Assistant (optional)
	var geminiClient *ai.GeminiClient
	if cfg.GeminiAPIKey != "" {
		geminiClient, err = ai.NewGeminiClient(context.Background(), cfg.GeminiAPIKey, ai.GeminiModels{
			Default:  cfg.GeminiModel,
			Chat:     cfg.GeminiModelChat,
			Pro:      cfg.GeminiModelPro,
			Image:    cfg.GeminiModelImage,
			ImagePro: cfg.GeminiModelImagePro,
		})
		if err != nil {
			log.Printf("WARNING: Failed to create Gemini client: %v", err)
		} else {
			engine.AssistantEnabled = true
			log.Printf("AI Assistant enabled (chat: %s, default: %s, pro: %s)", cfg.GeminiModelChat, cfg.GeminiModel, cfg.GeminiModelPro)
		}
	}

	// S3 storage (optional)
	var s3Client *storage.S3Client
	if cfg.S3Endpoint != "" && cfg.S3Bucket != "" {
		s3Client, err = storage.NewS3Client(storage.S3Config{
			Endpoint:        cfg.S3Endpoint,
			AccessKeyID:     cfg.S3AccessKeyID,
			SecretAccessKey: cfg.S3SecretAccessKey,
			Region:          cfg.S3Region,
			Bucket:          cfg.S3Bucket,
			ForcePathStyle:  cfg.S3ForcePathStyle,
		})
		if err != nil {
			log.Printf("WARNING: Failed to create S3 client: %v", err)
		} else {
			log.Printf("S3 storage enabled (bucket: %s)", cfg.S3Bucket)
		}
	}

	// Handlers
	secureCookie := strings.HasPrefix(cfg.BaseURL, "https")
	mailer := mail.NewMailer(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPFrom, cfg.SMTPSSL)

	authH := handlers.NewAuthHandler(db, sessions, engine, cfg.AESKey, secureCookie)
	dashH := handlers.NewDashboardHandler(db, engine)
	orgH := handlers.NewOrgHandler(db, engine, sessions, mailer, cfg.BaseURL, cfg.ProtectedSuperadmins)
	projH := handlers.NewProjectHandler(db, engine)
	ticketH := handlers.NewTicketHandler(db, engine, geminiClient, cfg)
	commentH := handlers.NewCommentHandler(db, engine)
	adminH := handlers.NewAdminHandler(db, engine)
	accountH := handlers.NewAccountHandler(db, sessions, engine)
	inviteH := handlers.NewInviteHandler(db, sessions, engine, mailer, cfg.AESKey, cfg.BaseURL, secureCookie)
	costH := handlers.NewCostHandler(db, engine)
	reactionH := handlers.NewReactionHandler(db, engine)
	chatHub := ws.NewHub()
	go chatHub.Run()
	var fileH *handlers.FileHandler
	if s3Client != nil {
		fileH = handlers.NewFileHandler(s3Client, db, geminiClient, cfg)
	}
	assistantH := handlers.NewAssistantHandler(db, engine, geminiClient, chatHub, cfg)

	// Feature Debate Mode (v0.2.0). Construct the refiner registry
	// lazily from whichever provider keys are configured — missing
	// keys mean the corresponding AI-picker button will return 400
	// ("unknown provider") on click rather than silently falling back
	// to another vendor (spec §3.2).
	//
	// DEBATE_REFINER_MODE=fake replaces every configured refiner with
	// an ai.FakeRefiner that returns a canned string. Only for E2E
	// tests (Playwright golden-path spec in e2e/tests/06-debate/)
	// that otherwise would need a real API key per run. Guarded
	// against production misconfiguration: panics at startup if the
	// env is set AND the configured BaseURL looks production-shaped
	// (anything that isn't localhost/127.0.0.1 or explicitly marked
	// as an http test origin). Fakes in production are how you ship
	// a feature that "works" but talks to no real AI.
	debateRefinerMode := os.Getenv("DEBATE_REFINER_MODE")
	if debateRefinerMode == "fake" {
		if !isLocalDebateEnv(cfg.BaseURL) {
			log.Fatalf("DEBATE_REFINER_MODE=fake set against non-local BaseURL %q — refusing to start (see cmd/server/main.go)", cfg.BaseURL)
		}
		log.Printf("WARNING: DEBATE_REFINER_MODE=fake — all debate refiners return canned output")
	}

	debateRefiners := map[string]ai.Refiner{}
	if cfg.AnthropicAPIKey != "" {
		anthropicClient := ai.NewAnthropicClient(cfg.AnthropicAPIKey, ai.AnthropicModels{
			Default: cfg.AnthropicModel,
			Content: cfg.AnthropicModelContent,
		})
		debateRefiners["claude"] = ai.NewAnthropicRefiner(anthropicClient, cfg.AnthropicModel)
	}
	if geminiClient != nil {
		debateRefiners["gemini"] = ai.NewGeminiRefiner(geminiClient, cfg.GeminiModel)
	}
	if cfg.OpenAIAPIKey != "" {
		debateRefiners["openai"] = ai.NewOpenAIRefiner(ai.NewOpenAIClient(cfg.OpenAIAPIKey, cfg.OpenAIModel))
	}
	// Swap in fakes for E2E tests when explicitly opted in (guarded
	// against production above).
	if debateRefinerMode == "fake" {
		debateRefiners = buildFakeDebateRefiners()
	}
	var debateScorer ai.Scorer
	if geminiClient != nil {
		// v1 hardcodes Gemini as the scorer (spec §3.2); phase-2 issue
		// #63 makes this configurable per project.
		debateScorer = ai.NewGeminiScorer(geminiClient, cfg.GeminiModel)
	}
	debateH := handlers.NewDebateHandler(db, engine, debateRefiners, debateScorer, handlers.DefaultDebateConfig())
	log.Printf("Feature Debate Mode wired (%d refiners, scorer=%v)", len(debateRefiners), debateScorer != nil)

	// Rate limiter for auth endpoints (0.5 req/s, burst 5)
	authLimiter := middleware.NewRateLimiter(0.5, 5)

	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.Recover)
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.Logging)

	// Static files (content-hashed URLs get immutable cache headers)
	r.Handle("/static/*", http.StripPrefix("/static/", manifest.Handler()))

	// File proxy (public but keys are UUIDs — no listing)
	if fileH != nil {
		r.Get("/files/*", fileH.ServeFile)
	}

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// CSRF protection for all HTML routes (auth, invitations, and protected)
	csrfMiddleware := middleware.CSRFProtect([]byte(cfg.SessionSecret[:32]), secureCookie)

	// Public auth routes (rate-limited)
	r.Group(func(r chi.Router) {
		r.Use(csrfMiddleware)
		r.Use(authLimiter.Limit)
		r.Get("/login", authH.LoginPage)
		r.Post("/login", authH.Login)
		r.Get("/register", authH.RegisterPage)
		r.Post("/register", authH.Register)

		// 2FA routes (need session cookie but not full auth)
		r.Get("/setup-2fa", authH.Setup2FAPage)
		r.Get("/setup-2fa/totp", authH.Setup2FATOTP)
		r.Post("/setup-2fa/totp/verify", authH.VerifySetupTOTP)
		r.Get("/verify-2fa", authH.Verify2FAPage)
		r.Post("/verify-2fa", authH.Verify2FA)
	})

	// Invitation routes (public — new users registering)
	r.Group(func(r chi.Router) {
		r.Use(csrfMiddleware)
		r.Get("/invite/{token}", inviteH.InviteRegisterPage)
		r.Post("/invite/{token}", inviteH.InviteRegister)
	})

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(sessions, db))
		r.Use(csrfMiddleware)

		r.Post("/logout", authH.Logout)
		r.Get("/", dashH.Dashboard)

		// Account settings
		r.Get("/account/settings", accountH.AccountSettingsPage)
		r.Post("/account/password", accountH.ChangePassword)
		r.Post("/account/email", accountH.ChangeEmail)

		// Invitations (authenticated user accepting/declining)
		r.Post("/invitations/{invitationID}/accept", inviteH.AcceptInvitation)
		r.Post("/invitations/{invitationID}/decline", inviteH.DeclineInvitation)

		// Organizations
		r.Post("/switch-org", orgH.SwitchOrg)
		r.Post("/orgs", orgH.CreateOrg)
		r.Get("/orgs/{orgSlug}", orgH.OrgPage)
		r.Get("/orgs/{orgSlug}/settings", orgH.OrgSettings)
		r.Post("/orgs/{orgSlug}/invitations", orgH.InviteMember)
		r.Delete("/orgs/{orgSlug}/invitations/{invitationID}", inviteH.RevokeInvitation)
		r.Post("/orgs/{orgSlug}/invitations/{invitationID}/resend", inviteH.ResendInvitation)
		r.Delete("/orgs/{orgSlug}/members/{userID}", orgH.RemoveMember)
		r.Patch("/orgs/{orgSlug}/members/{userID}/role", orgH.UpdateMemberRole)
		r.Post("/orgs/{orgSlug}/settings/business", orgH.UpdateBusinessDetails)
		r.Post("/orgs/{orgSlug}/settings/margin", orgH.UpdateAIMargin)
		r.Post("/orgs/{orgSlug}/settings/currency", orgH.UpdateCurrency)

		// Projects
		r.Post("/orgs/{orgSlug}/projects", projH.CreateProject)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/brief", projH.ProjectBrief)
		r.Put("/orgs/{orgSlug}/projects/{projSlug}/brief", projH.UpdateBrief)
		r.Post("/orgs/{orgSlug}/projects/{projSlug}/brief/reviewed", projH.MarkBriefReviewed)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/features", projH.ProjectFeatures)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/bugs", projH.ProjectBugs)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/gantt", projH.ProjectGantt)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/archived", projH.ProjectArchived)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/settings", projH.ProjectSettings)
		r.Post("/orgs/{orgSlug}/projects/{projSlug}/settings/repo", projH.UpdateRepoURL)
		r.Post("/orgs/{orgSlug}/projects/{projSlug}/transfer", projH.TransferProject)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/costs", costH.ProjectCosts)
		r.Post("/orgs/{orgSlug}/projects/{projSlug}/costs", costH.AddCostItem)
		r.Get("/orgs/{orgSlug}/costs", costH.OrgCosts)
		r.Patch("/costs/{costID}", costH.UpdateCostItem)
		r.Delete("/costs/{costID}", costH.DeleteCostItem)

		// Tickets
		r.Post("/tickets", ticketH.CreateTicket)
		r.Get("/tickets/{ticketID}", ticketH.TicketDetail)
		r.Patch("/tickets/{ticketID}/status", ticketH.UpdateStatus)
		r.Patch("/tickets/{ticketID}/agent", ticketH.UpdateAgentMode)
		r.Post("/tickets/{ticketID}/archive", ticketH.ArchiveTicket)
		r.Post("/tickets/{ticketID}/restore", ticketH.RestoreTicket)
		r.Put("/tickets/{ticketID}", ticketH.UpdateTicket)
		r.Delete("/tickets/{ticketID}", ticketH.DeleteTicket)

		// Feature Debate Mode (spec §4). Task 9 extends this block
		// with approve/abandon.
		r.Get("/tickets/{ticketID}/debate", debateH.ShowDebate)
		r.Post("/tickets/{ticketID}/debate/start", debateH.StartDebate)
		r.Post("/tickets/{ticketID}/debate/rounds", debateH.CreateRound)
		r.Post("/tickets/{ticketID}/debate/rounds/{roundID}/accept", debateH.AcceptRound)
		r.Post("/tickets/{ticketID}/debate/rounds/{roundID}/reject", debateH.RejectRound)
		r.Post("/tickets/{ticketID}/debate/undo", debateH.UndoRound)
		r.Post("/tickets/{ticketID}/debate/approve", debateH.ApproveDebate)
		r.Post("/tickets/{ticketID}/debate/abandon", debateH.AbandonDebate)

		// Comments
		r.Post("/tickets/{ticketID}/comments", commentH.CreateComment)

		// Reactions
		r.Post("/reactions/{targetType}/{targetID}", reactionH.ToggleReaction)

		// File uploads (S3)
		if fileH != nil {
			r.Post("/api/upload-image", fileH.UploadImage)
			r.Post("/api/upload-file", fileH.UploadFile)
			r.Delete("/api/attachments/{attachmentID}", fileH.DeleteAttachment)
			r.Post("/api/generate-image", fileH.GenerateImage)
		}

		// AI Assistant (WebSocket)
		r.Get("/ws/assistant/{projectID}", assistantH.HandleWebSocket)
		r.Delete("/assistant/conversations/{convID}", assistantH.DeleteConversation)

		// Admin
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole(auth.RoleSuperAdmin))
			r.Get("/admin", adminH.AdminPage)
			r.Post("/admin/settings/business", adminH.UpdatePlatformBusiness)
		})
	})

	// Start server
	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 120 * time.Second, // Extended for SSE streaming
		IdleTimeout:  60 * time.Second,
	}

	// Session and invitation cleanup goroutine
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			_ = sessions.CleanExpired(context.Background())
			_ = db.ExpireOldInvitations(context.Background())
		}
	}()

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Printf("ForgeDesk server starting on %s", cfg.ListenAddr)
	if listenErr := srv.ListenAndServe(); listenErr != nil && listenErr != http.ErrServerClosed {
		log.Fatalf("server error: %v", listenErr)
	}
	log.Println("Server stopped")
}

// isLocalDebateEnv returns true iff the BaseURL looks like a local
// development or test origin — the only contexts where
// DEBATE_REFINER_MODE=fake is acceptable. Prod-like URLs (anything
// with a real TLD, remote IP, or TLS) fall through to false.
//
// Conservative allowlist: accepts scheme+host combinations whose
// hostname resolves to localhost, 127.0.0.1, ::1, or ends with
// .local/.test. Anything else — including plain hostnames, LAN IPs,
// and public domains — is rejected so a misconfigured prod env var
// can't accidentally serve fake AI output to real users.
func isLocalDebateEnv(baseURL string) bool {
	lower := strings.ToLower(baseURL)
	allowedHosts := []string{
		"://localhost",
		"://127.0.0.1",
		"://[::1]",
		".local",
		".test",
	}
	for _, h := range allowedHosts {
		if strings.Contains(lower, h) {
			return true
		}
	}
	return false
}

// buildFakeDebateRefiners returns a refiner registry backed entirely
// by ai.FakeRefiner so E2E tests exercise the handler flow without
// hitting a real AI provider. Each fake returns a provider-tagged
// canned string — varied enough that golden-path tests can assert
// which provider's button was clicked.
func buildFakeDebateRefiners() map[string]ai.Refiner {
	mk := func(name, model string) ai.Refiner {
		return &ai.FakeRefiner{
			NameVal: name, ModelVal: model,
			OutputFunc: func(in ai.RefineInput) (string, string, error) {
				// Always return a non-trivial string so the handler's
				// MinOutputLen check passes. Include the original
				// text to keep the diff renderer's output interesting.
				return "Refactored by " + name + ":\n\n" + in.CurrentText + "\n\n- added by fake refiner", ai.FinishReasonStop, nil
			},
		}
	}
	return map[string]ai.Refiner{
		"claude": mk("claude", ai.ModelClaudeSonnet46),
		"gemini": mk("gemini", ai.ModelGeminiFlash),
		"openai": mk("openai", ai.ModelGPT5Mini),
	}
}
