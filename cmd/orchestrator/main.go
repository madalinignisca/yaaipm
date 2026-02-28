package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/models"
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

	db := models.NewDB(pool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Println("ForgeDesk orchestrator starting...")

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			processTickets(ctx, db)
		case <-sigCh:
			log.Println("Orchestrator shutting down...")
			return
		}
	}
}

func processTickets(ctx context.Context, db *models.DB) {
	tickets, err := db.ListAgentReady(ctx)
	if err != nil {
		log.Printf("fetching agent-ready tickets: %v", err)
		return
	}

	if len(tickets) == 0 {
		return
	}

	log.Printf("Found %d tickets ready for agent processing", len(tickets))

	for _, t := range tickets {
		agentMode := "plan"
		if t.AgentMode != nil {
			agentMode = *t.AgentMode
		}

		agentName := "claude"
		if t.AgentName != nil {
			agentName = *t.AgentName
		}

		log.Printf("Processing ticket %s: mode=%s agent=%s title=%q", t.ID, agentMode, agentName, t.Title)

		// For now, just log — actual agent dispatch will be implemented later
		// This creates comments on the ticket to show the orchestrator is working
		var comment string
		switch agentMode {
		case "plan":
			comment = "[Orchestrator] Planning phase initiated. Agent " + agentName + " will analyze this ticket and produce an implementation plan."
			db.UpdateTicketStatus(ctx, t.ID, "planning")
		case "implement":
			comment = "[Orchestrator] Implementation phase initiated. Agent " + agentName + " will implement the approved plan."
			db.UpdateTicketStatus(ctx, t.ID, "implementing")
		}

		if comment != "" {
			db.CreateComment(ctx, t.ID, nil, &agentName, comment)
		}
	}
}
