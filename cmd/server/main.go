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
