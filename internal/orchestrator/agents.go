package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/models"
)

// runAgentCLI shells out to the specified agent CLI in the given workDir.
// Returns the agent's stdout output.
func runAgentCLI(ctx context.Context, agentName, workDir, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	var cmd *exec.Cmd
	switch agentName {
	case "claude":
		cmd = exec.CommandContext(ctx, "claude", "-p", prompt, "--output-format", "text")
	case "gemini":
		cmd = exec.CommandContext(ctx, "gemini", "-p", prompt)
	case "codex":
		cmd = exec.CommandContext(ctx, "codex", prompt, "--approval-mode", "full-auto")
	case "vibe":
		cmd = exec.CommandContext(ctx, "vibe", "--prompt", prompt, "--agent", "auto-approve")
	default:
		return "", fmt.Errorf("unsupported agent: %s", agentName)
	}

	cmd.Dir = workDir
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("agent %s exited with error: %w (stderr: %s)", agentName, err, truncate(stderr.String(), 500))
	}
	return stdout.String(), nil
}

// commitAndPush stages all changes, commits with a ticket-linked message, and pushes.
// Returns true if a commit was created, false if there were no changes.
func commitAndPush(ctx context.Context, workDir string, ticket models.Ticket, branchName string) (bool, error) {
	// Stage all changes
	if _, err := runGit(ctx, workDir, "add", "-A"); err != nil {
		return false, fmt.Errorf("git add: %w", err)
	}

	// Check if there's anything to commit
	diff, err := runGit(ctx, workDir, "diff", "--cached", "--stat")
	if err != nil {
		return false, fmt.Errorf("git diff: %w", err)
	}
	if strings.TrimSpace(diff) == "" {
		return false, nil // Nothing to commit
	}

	// Build commit message
	shortID := ticketShortID(ticket.ID)
	msg := fmt.Sprintf("[T-%s] %s\n\nImplemented by ForgeDesk orchestrator agent.", shortID, ticket.Title)

	if _, err := runGit(ctx, workDir, "commit", "-m", msg); err != nil {
		return false, fmt.Errorf("git commit: %w", err)
	}

	// Push to remote
	if _, err := runGit(ctx, workDir, "push", "-u", "origin", branchName); err != nil {
		return false, fmt.Errorf("git push: %w", err)
	}

	return true, nil
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
