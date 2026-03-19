package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/orchestrator"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	if cfg.AnthropicAPIKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is required for the orchestrator")
	}

	// Ensure workspaces directory exists
	if mkdirErr := os.MkdirAll(cfg.WorkspacesDir, 0o750); mkdirErr != nil {
		log.Fatalf("creating workspaces dir %s: %v", cfg.WorkspacesDir, mkdirErr)
	}
	log.Printf("Workspaces directory: %s", cfg.WorkspacesDir)

	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("pinging database: %v", err) //nolint:gocritic // exitAfterDefer - acceptable at startup
	}

	db := models.NewDB(pool)

	claude := ai.NewAnthropicClient(cfg.AnthropicAPIKey, ai.AnthropicModels{
		Default: cfg.AnthropicModel,
		Content: cfg.AnthropicModelContent,
	})

	dispatcher := orchestrator.NewDispatcher(db, claude, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("ForgeDesk orchestrator starting (model: %s)", cfg.AnthropicModel)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dispatcher.ProcessTickets(ctx)
		case <-sigCh:
			log.Println("Orchestrator shutting down...")
			return
		}
	}
}
