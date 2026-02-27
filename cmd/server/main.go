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

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/handlers"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("pinging database: %v", err)
	}
	log.Println("Database connected")

	db := models.NewDB(pool)
	sessions := auth.NewSessionStore(pool)

	engine, err := render.NewEngine("templates")
	if err != nil {
		log.Fatalf("loading templates: %v", err)
	}
	log.Println("Templates loaded")

	// Handlers
	secureCookie := strings.HasPrefix(cfg.BaseURL, "https")
	authH := handlers.NewAuthHandler(db, sessions, engine, cfg.AESKey, secureCookie)
	dashH := handlers.NewDashboardHandler(db, engine)
	orgH := handlers.NewOrgHandler(db, engine)
	projH := handlers.NewProjectHandler(db, engine)
	ticketH := handlers.NewTicketHandler(db, engine)
	commentH := handlers.NewCommentHandler(db, engine)
	adminH := handlers.NewAdminHandler(db, engine)

	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.Recover)
	r.Use(middleware.Logging)

	// Static files
	fileServer := http.FileServer(http.Dir("static"))
	r.Handle("/static/*", http.StripPrefix("/static/", fileServer))

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Public auth routes
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

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(sessions, db))

		r.Post("/logout", authH.Logout)
		r.Get("/", dashH.Dashboard)

		// Organizations
		r.Post("/orgs", orgH.CreateOrg)
		r.Get("/orgs/{orgSlug}", orgH.OrgPage)
		r.Get("/orgs/{orgSlug}/settings", orgH.OrgSettings)
		r.Post("/orgs/{orgSlug}/members", orgH.AddMember)
		r.Delete("/orgs/{orgSlug}/members/{userID}", orgH.RemoveMember)
		r.Patch("/orgs/{orgSlug}/members/{userID}/role", orgH.UpdateMemberRole)

		// Projects
		r.Post("/orgs/{orgSlug}/projects", projH.CreateProject)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/brief", projH.ProjectBrief)
		r.Put("/orgs/{orgSlug}/projects/{projSlug}/brief", projH.UpdateBrief)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/features", projH.ProjectFeatures)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/bugs", projH.ProjectBugs)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/gantt", projH.ProjectGantt)

		// Tickets
		r.Post("/tickets", ticketH.CreateTicket)
		r.Get("/tickets/{ticketID}", ticketH.TicketDetail)
		r.Patch("/tickets/{ticketID}/status", ticketH.UpdateStatus)
		r.Patch("/tickets/{ticketID}/agent", ticketH.UpdateAgentMode)

		// Comments
		r.Post("/tickets/{ticketID}/comments", commentH.CreateComment)

		// Admin
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole(auth.RoleSuperAdmin))
			r.Get("/admin", adminH.AdminPage)
		})
	})

	// Start server
	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Session cleanup goroutine
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			sessions.CleanExpired(context.Background())
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
		srv.Shutdown(ctx)
	}()

	log.Printf("ForgeDesk server starting on %s", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Println("Server stopped")
}
