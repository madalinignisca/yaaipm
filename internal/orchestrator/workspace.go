package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WorkspaceManager handles git repository cloning and worktree management.
//
// Directory layout:
//
//	{baseDir}/{orgSlug}/{projectSlug}/repo/                      ← main clone
//	{baseDir}/{orgSlug}/{projectSlug}/worktrees/agent-{id8}/     ← per-ticket worktree
type WorkspaceManager struct {
	baseDir string
}

// NewWorkspaceManager creates a WorkspaceManager rooted at baseDir.
func NewWorkspaceManager(baseDir string) *WorkspaceManager {
	return &WorkspaceManager{baseDir: baseDir}
}

// EnsureRepo clones the project repo if missing, or fetches+pulls if it exists.
// Returns the path to the repo directory.
func (wm *WorkspaceManager) EnsureRepo(ctx context.Context, orgSlug, projectSlug, repoURL string) (string, error) {
	if repoURL == "" {
		return "", fmt.Errorf("project %s/%s has no repo_url configured", orgSlug, projectSlug)
	}

	repoDir := filepath.Join(wm.baseDir, orgSlug, projectSlug, "repo")

	// Check if repo already exists
	if _, err := runGit(ctx, repoDir, "rev-parse", "--git-dir"); err == nil {
		// Repo exists — fetch and pull
		if _, err := runGit(ctx, repoDir, "fetch", "--all", "--prune"); err != nil {
			return "", fmt.Errorf("fetching repo: %w", err)
		}
		// Pull only if on a branch (not detached HEAD)
		if branch, err := runGit(ctx, repoDir, "symbolic-ref", "--short", "HEAD"); err == nil && strings.TrimSpace(branch) != "" {
			if _, err := runGit(ctx, repoDir, "pull", "--ff-only"); err != nil {
				// Non-fatal: might have diverged
			}
		}
		return repoDir, nil
	}

	// Clone fresh
	if _, err := runGit(ctx, wm.baseDir, "clone", repoURL, repoDir); err != nil {
		return "", fmt.Errorf("cloning repo %s: %w", repoURL, err)
	}

	return repoDir, nil
}

// CreateWorktree creates a git worktree for a ticket on a new branch.
// Returns the worktree directory path and branch name.
func (wm *WorkspaceManager) CreateWorktree(ctx context.Context, orgSlug, projectSlug, ticketID, ticketTitle string) (wtDir, branchName string, err error) {
	repoDir := filepath.Join(wm.baseDir, orgSlug, projectSlug, "repo")
	shortID := ticketShortID(ticketID)
	branchName = agentBranchName(shortID, ticketTitle)
	wtDir = filepath.Join(wm.baseDir, orgSlug, projectSlug, "worktrees", "agent-"+shortID)

	// Check if worktree already exists
	if _, err := runGit(ctx, wtDir, "rev-parse", "--git-dir"); err == nil {
		// Already exists — just check out the branch
		return wtDir, branchName, nil
	}

	// Try to create worktree with new branch based on origin/main
	defaultBranch := detectDefaultBranch(ctx, repoDir)
	_, err = runGit(ctx, repoDir, "worktree", "add", "-b", branchName, wtDir, "origin/"+defaultBranch)
	if err != nil {
		// Branch might already exist (retry from previous run)
		_, err = runGit(ctx, repoDir, "worktree", "add", wtDir, branchName)
		if err != nil {
			return "", "", fmt.Errorf("creating worktree: %w", err)
		}
	}

	return wtDir, branchName, nil
}

// RemoveWorktree removes a ticket's worktree.
func (wm *WorkspaceManager) RemoveWorktree(ctx context.Context, orgSlug, projectSlug, ticketID string) error {
	repoDir := filepath.Join(wm.baseDir, orgSlug, projectSlug, "repo")
	shortID := ticketShortID(ticketID)
	wtDir := filepath.Join(wm.baseDir, orgSlug, projectSlug, "worktrees", "agent-"+shortID)

	if _, err := runGit(ctx, repoDir, "worktree", "remove", "--force", wtDir); err != nil {
		return fmt.Errorf("removing worktree %s: %w", wtDir, err)
	}
	return nil
}

// ticketShortID returns the first 8 hex characters of a UUID (without dashes).
func ticketShortID(uuid string) string {
	clean := strings.ReplaceAll(uuid, "-", "")
	if len(clean) > 8 {
		return clean[:8]
	}
	return clean
}

// agentBranchName creates a branch name like "agent/a1b2c3d4-ticket-title-slug".
func agentBranchName(shortID, title string) string {
	slug := slugify(title)
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return "agent/" + shortID + "-" + slug
}

// slugify converts a title to a URL-safe slug.
var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// detectDefaultBranch tries to determine the remote's default branch (main or master).
func detectDefaultBranch(ctx context.Context, repoDir string) string {
	out, err := runGit(ctx, repoDir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(out)
		// refs/remotes/origin/main → main
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	// Fallback: check if origin/main exists
	if _, err := runGit(ctx, repoDir, "rev-parse", "--verify", "origin/main"); err == nil {
		return "main"
	}
	return "master"
}

// runGit runs a git command in the given directory and returns stdout.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
