# Debate UI Refactor ("Document Workspace") Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the Feature Debate page as a document-first workspace (rendered current description + suggestion panel with word-level diff + versions rail + polling effort chip), per the approved spec `docs/superpowers/specs/2026-06-12-debate-ui-refactor-design.md`.

**Architecture:** Backend invariants, existing routes, and the already-async scorer are untouched. We add a word-level diff renderer to `internal/diff`, a view-model layer in `internal/handlers`, 4 small endpoints (3 GET partials + seed edit POST), and rewrite the 5 debate templates into 7 new ones. Accept/Reject/Undo switch from `Hx-Refresh`/`Hx-Redirect` full reloads to HTMX partial + out-of-band (OOB) swaps.

**Tech Stack:** Go 1.25 (`gvm`), chi, pgx (raw SQL), `html/template` SSR, HTMX (`hx-boost`), Alpine.js inline, DaisyUI v5 / Tailwind v4 (`scripts/css.sh`), `sergi/go-diff`, Playwright E2E.

**Branch:** create `feature/debate-ui-workspace` off `main` before Task 1.

**Test environment (needed for all `go test` steps):**
```bash
docker compose -f docker-compose.test.yml up -d postgres
docker compose -f docker-compose.test.yml up migrate
# Always: go test ./internal/... -p 1 -count=1 -timeout 120s   (-p 1 is REQUIRED — shared test DB)
```

**Codebase facts the spec doesn't repeat (read this first):**
- Model IDs are `string`, not `uuid.UUID` (`models.FeatureDebate.ID string`). The spec's `versionEntry` sketch shows `uuid.UUID`; use `string` everywhere.
- `GetDebateRounds` returns rounds ordered `round_number ASC`.
- `engine.RenderPartial(w, "name.html", data)` looks up `partial:name.html` — every file in `templates/components/` auto-registers. Multiple calls on one `ResponseWriter` concatenate — that is how we emit primary + OOB fragments.
- FuncMap already has: `markdown`, `relTime`, `derefInt`, `derefStr`, `dict`, `csrfField`, `providerLabel`, `providerBadgeClass`, `truncate`, `mul`, `eq`.
- **PR #80 gotcha:** a conditional bare attribute inside a tag (`{{if X}}checked{{end}}`) can silently truncate `html/template` output. For conditional `hx-swap-oob`, always duplicate the whole opening tag in an `{{if}}…{{else}}…{{end}}`.
- Handler tests use `setupDebateTestEnv(t)` + `seedAuthedFeatureTicket(t, db, sessions)` + `startAndCreateRounds(...)` from `internal/handlers/debate_test.go`. Reuse them; don't invent new harnesses.
- The scorer is ALREADY async (`scoreAfterAccept`, `debate.go:631`). Do not touch it.

---

### Task 1: `diff.RenderInlineHTML` — word-level inline diff

**Files:**
- Modify: `internal/diff/diff.go`
- Test: `internal/diff/diff_test.go`
- Modify: `static/css/tw-input.css` (styles for `.diff-inline`)

- [ ] **Step 1: Write the failing tests** — append to `internal/diff/diff_test.go`:

```go
func TestRenderInlineHTML_WordLevelInsert(t *testing.T) {
	got := string(RenderInlineHTML("The quick fox jumps.", "The quick brown fox jumps."))
	if !strings.Contains(got, `<ins class="diff-ins">`) {
		t.Fatalf("expected <ins> for insertion, got: %s", got)
	}
	if strings.Contains(got, "<del") {
		t.Fatalf("pure insertion must not produce <del>, got: %s", got)
	}
	if !strings.Contains(got, "brown") {
		t.Fatalf("inserted word missing, got: %s", got)
	}
}

func TestRenderInlineHTML_WordLevelDelete(t *testing.T) {
	got := string(RenderInlineHTML("uses basic logging only today", "uses logging today"))
	if !strings.Contains(got, `<del class="diff-del">`) {
		t.Fatalf("expected <del> for deletion, got: %s", got)
	}
}

func TestRenderInlineHTML_EscapesScriptTags(t *testing.T) {
	got := string(RenderInlineHTML("before", `before <script>alert(1)</script>`))
	if strings.Contains(got, "<script>") {
		t.Fatalf("script tag not escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag, got: %s", got)
	}
}

func TestRenderInlineHTML_EscapesAttributeInjection(t *testing.T) {
	got := string(RenderInlineHTML("a", `a " onmouseover="alert(1)`))
	if strings.Contains(got, `" onmouseover="`) {
		t.Fatalf("attribute injection survived: %s", got)
	}
}

func TestRenderInlineHTML_PreservesNewlines(t *testing.T) {
	// Newlines must survive escaping verbatim — the container renders
	// with white-space: pre-wrap, so "\n" is the line-break mechanism.
	got := string(RenderInlineHTML("## Title\n- item one", "## Title\n- item one\n- item two"))
	if !strings.Contains(got, "\n") {
		t.Fatalf("newlines lost — pre-wrap rendering will collapse lines: %s", got)
	}
	if strings.Contains(got, "<br") {
		t.Fatalf("renderer must not invent <br> tags: %s", got)
	}
}

func TestRenderInlineHTML_Unicode(t *testing.T) {
	got := string(RenderInlineHTML("naïve café", "naïve café ☕ déjà"))
	if !strings.Contains(got, "☕") || !strings.Contains(got, "déjà") {
		t.Fatalf("unicode mangled: %s", got)
	}
}

func TestRenderInlineHTML_IdenticalInputs(t *testing.T) {
	got := string(RenderInlineHTML("same text", "same text"))
	if strings.Contains(got, "<ins") || strings.Contains(got, "<del") {
		t.Fatalf("identical inputs must produce no ins/del: %s", got)
	}
}

func TestRenderInlineHTML_EmptyInputs(t *testing.T) {
	got := string(RenderInlineHTML("", ""))
	if !strings.Contains(got, `class="diff-inline"`) {
		t.Fatalf("expected empty wrapper, got: %s", got)
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/diff/ -run TestRenderInlineHTML -v`
Expected: FAIL — `undefined: RenderInlineHTML`

- [ ] **Step 3: Implement** — append to `internal/diff/diff.go`:

```go
// RenderInlineHTML diffs before→after at word/phrase granularity and
// returns sanitized HTML: unchanged text plain, insertions wrapped in
// <ins class="diff-ins">, deletions in <del class="diff-del">. The
// container div uses white-space: pre-wrap (see tw-input.css) so the
// escaped newlines preserve markdown's line structure without any <br>
// rewriting here.
//
// DiffCleanupSemantic merges character-level noise into human-readable
// runs — that is what makes prose diffs legible vs. the line-level
// unified output of ComputeUnified (kept for the audit trail).
//
// Every text segment is routed through template.HTMLEscapeString; the
// only literal HTML is the hardcoded wrapper/ins/del tags, so the
// template.HTML cast is sound. Pinned by
// TestRenderInlineHTML_EscapesScriptTags / _EscapesAttributeInjection.
func RenderInlineHTML(before, after string) template.HTML {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(before, after, false)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var sb strings.Builder
	sb.WriteString(`<div class="diff-inline">`)
	for _, d := range diffs {
		text := template.HTMLEscapeString(d.Text)
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			sb.WriteString(text)
		case diffmatchpatch.DiffInsert:
			sb.WriteString(`<ins class="diff-ins">`)
			sb.WriteString(text)
			sb.WriteString(`</ins>`)
		case diffmatchpatch.DiffDelete:
			sb.WriteString(`<del class="diff-del">`)
			sb.WriteString(text)
			sb.WriteString(`</del>`)
		}
	}
	sb.WriteString(`</div>`)
	return template.HTML(sb.String()) // nosemgrep: go.lang.security.audit.xss.template-html-does-not-escape.unsafe-template-type
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/diff/ -v`
Expected: all PASS (including the pre-existing ComputeUnified/RenderHTML tests — RenderHTML is deleted later, in Task 9).

- [ ] **Step 5: Add the CSS** — append to `static/css/tw-input.css` (after the existing custom rules):

```css
/* Debate inline diff (diff.RenderInlineHTML output). pre-wrap is what
   keeps markdown line structure — the renderer emits raw \n, never <br>. */
.diff-inline {
  white-space: pre-wrap;
  line-height: 1.7;
  overflow-wrap: anywhere;
}
.diff-inline ins.diff-ins {
  text-decoration: none;
  border-radius: 0.25rem;
  padding: 0 0.125rem;
  background: color-mix(in oklab, var(--color-success) 25%, transparent);
}
.diff-inline del.diff-del {
  border-radius: 0.25rem;
  padding: 0 0.125rem;
  background: color-mix(in oklab, var(--color-error) 18%, transparent);
}
```

- [ ] **Step 6: Rebuild CSS, verify it compiles**

Run: `bash scripts/css.sh`
Expected: exits 0, regenerates `static/css/tw.css` (grep it: `grep -c "diff-inline" static/css/tw.css` ≥ 1).

- [ ] **Step 7: Commit**

```bash
git add internal/diff/ static/css/tw-input.css static/css/tw.css
git commit -m "feat(debate): word-level inline diff renderer (RenderInlineHTML)"
```

---

### Task 2: `renderInlineDiff` FuncMap helper

**Files:**
- Modify: `internal/render/render.go` (FuncMap, next to the existing `renderDiff` entry ~line 256 — leave `renderDiff` in place; it dies in Task 9)
- Test: `internal/render/render_test.go`

- [ ] **Step 1: Write the failing test** — append to `internal/render/render_test.go`, following the file's existing FuncMap-test pattern (grep `renderDiff` there for the model):

```go
func TestFuncMap_RenderInlineDiff(t *testing.T) {
	fm := newFuncMap()
	fn, ok := fm["renderInlineDiff"].(func(before, after string) template.HTML)
	if !ok {
		t.Fatal("renderInlineDiff not registered with expected signature")
	}
	got := string(fn("a b", "a c b"))
	if !strings.Contains(got, "<ins") {
		t.Fatalf("expected ins markup, got: %s", got)
	}
}
```

(If the FuncMap constructor has a different name than `newFuncMap`, match whatever `render.go` actually uses — find it with `grep -n "FuncMap{" internal/render/render.go`.)

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/render/ -run TestFuncMap_RenderInlineDiff -v`
Expected: FAIL — key not present.

- [ ] **Step 3: Implement** — in `internal/render/render.go`, directly after the `"renderDiff"` entry:

```go
		// renderInlineDiff renders a word-level prose diff between two
		// texts (debate suggestion "What changed" tab). Sanitization
		// lives in diff.RenderInlineHTML — see that package's audit.
		"renderInlineDiff": func(before, after string) template.HTML {
			return diff.RenderInlineHTML(before, after)
		},
```

- [ ] **Step 4: Run tests, verify pass:** `go test ./internal/render/ -p 1 -count=1 -timeout 120s` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/render/
git commit -m "feat(render): renderInlineDiff template helper"
```

---

### Task 3: View model — `internal/handlers/debate_view.go`

Pure functions over `(debate, rounds)`; no DB, no HTTP. This is where version labels, staleness, and staff gating are decided.

**Files:**
- Create: `internal/handlers/debate_view.go`
- Test: `internal/handlers/debate_view_test.go` (pure unit tests — no `setupDebateTestEnv`, no DB)

- [ ] **Step 1: Write the failing tests** — create `internal/handlers/debate_view_test.go`:

```go
package handlers

import (
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/models"
)

func strPtr(s string) *string { return &s }
func timePtr(t time.Time) *time.Time { return &t }

func mkRound(id string, n int, status, provider, output string, decided *time.Time) models.DebateRound {
	cost := int64(12345)
	return models.DebateRound{
		ID: id, RoundNumber: n, Status: status, Provider: provider,
		Model: "model-x", OutputText: output, InputText: "in",
		DecidedAt: decided, CostMicros: &cost,
	}
}

func TestBuildDebateView_VersionLabelsCountAcceptedOnly(t *testing.T) {
	now := time.Now()
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive, CurrentText: "v2 text"}
	rounds := []models.DebateRound{
		mkRound("r1", 1, "accepted", "claude", "v1 text", timePtr(now.Add(-20*time.Minute))),
		mkRound("r2", 2, "rejected", "openai", "rejected text", timePtr(now.Add(-5*time.Minute))),
		mkRound("r3", 3, "accepted", "gemini", "v2 text", timePtr(now.Add(-5*time.Minute))),
	}
	v := buildDebateView(deb, rounds, false)

	if v.CurrentVersionLabel != 2 {
		t.Fatalf("CurrentVersionLabel = %d, want 2", v.CurrentVersionLabel)
	}
	// Newest-first: r3(v2), r2(dismissed), r1(v1)
	if len(v.Versions) != 3 {
		t.Fatalf("len(Versions) = %d, want 3", len(v.Versions))
	}
	if v.Versions[0].RoundID != "r3" || v.Versions[0].VersionLabel != 2 || !v.Versions[0].IsCurrent {
		t.Fatalf("Versions[0] wrong: %+v", v.Versions[0])
	}
	if v.Versions[1].RoundID != "r2" || v.Versions[1].Accepted || v.Versions[1].VersionLabel != 0 {
		t.Fatalf("Versions[1] (dismissed) wrong: %+v", v.Versions[1])
	}
	if v.Versions[2].VersionLabel != 1 || v.Versions[2].RestoreFrom != 2 {
		t.Fatalf("Versions[2] wrong (RestoreFrom must be RoundNumber+1): %+v", v.Versions[2])
	}
	if v.Pending != nil {
		t.Fatal("no in_review round — Pending must be nil")
	}
	if v.LatestAcceptedRoundID != "r3" {
		t.Fatalf("LatestAcceptedRoundID = %q, want r3", v.LatestAcceptedRoundID)
	}
}

func TestBuildDebateView_PendingSuggestion(t *testing.T) {
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive, CurrentText: "seed"}
	rounds := []models.DebateRound{mkRound("r1", 1, "in_review", "claude", "proposal", nil)}
	v := buildDebateView(deb, rounds, false)

	if v.Pending == nil || v.Pending.RoundID != "r1" || v.Pending.NextVersion != 1 {
		t.Fatalf("Pending wrong: %+v", v.Pending)
	}
	if v.CurrentVersionLabel != 0 {
		t.Fatalf("CurrentVersionLabel = %d, want 0 (no accepted rounds)", v.CurrentVersionLabel)
	}
	// in_review rounds never appear in the rail
	if len(v.Versions) != 0 {
		t.Fatalf("in_review round leaked into Versions: %+v", v.Versions)
	}
	if v.CanEditSeed {
		t.Fatal("CanEditSeed must be false once any round exists")
	}
}

func TestBuildDebateView_CanEditSeedOnlyWhenNoRoundsAndNotInFlight(t *testing.T) {
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive}
	if v := buildDebateView(deb, nil, false); !v.CanEditSeed {
		t.Fatal("no rounds + not in flight → CanEditSeed must be true")
	}
	deb.InFlightRequestID = strPtr("req-1")
	if v := buildDebateView(deb, nil, false); v.CanEditSeed {
		t.Fatal("in-flight reservation → CanEditSeed must be false")
	}
}

func TestBuildDebateView_StaffGating(t *testing.T) {
	now := time.Now()
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive}
	rounds := []models.DebateRound{mkRound("r1", 1, "accepted", "claude", "x", timePtr(now))}

	client := buildDebateView(deb, rounds, false)
	if client.Versions[0].Model != "" || client.Versions[0].CostMicros != 0 {
		t.Fatalf("client must not see model/cost: %+v", client.Versions[0])
	}
	staff := buildDebateView(deb, rounds, true)
	if staff.Versions[0].Model != "model-x" || staff.Versions[0].CostMicros != 12345 {
		t.Fatalf("staff must see model/cost: %+v", staff.Versions[0])
	}
}

func TestBuildEffortChipView_StaleAndPolling(t *testing.T) {
	now := time.Now()
	recent := timePtr(now.Add(-10 * time.Second))
	old := timePtr(now.Add(-5 * time.Minute))
	window := 90 * time.Second

	// Accepted round not yet scored, accept was recent → rescoring + poll.
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive}
	rounds := []models.DebateRound{mkRound("r1", 1, "accepted", "claude", "x", recent)}
	chip := buildEffortChipView(deb, rounds, "t1", now, window)
	if !chip.Rescoring || !chip.Poll {
		t.Fatalf("recent unscored accept: want Rescoring+Poll, got %+v", chip)
	}

	// Same but accept is old → rescoring shown as settled, no poll.
	rounds[0].DecidedAt = old
	chip = buildEffortChipView(deb, rounds, "t1", now, window)
	if chip.Poll {
		t.Fatalf("accept older than window must not poll: %+v", chip)
	}

	// Scored round matches latest accepted → fresh, no poll.
	deb.LastScoredRoundID = strPtr("r1")
	score := 6
	deb.EffortScore = &score
	rounds[0].DecidedAt = recent
	chip = buildEffortChipView(deb, rounds, "t1", now, window)
	if chip.Rescoring || chip.Poll {
		t.Fatalf("fresh score must be neither rescoring nor polling: %+v", chip)
	}

	// Terminal debate never polls, even when stale & recent.
	deb.LastScoredRoundID = nil
	deb.Status = "approved"
	chip = buildEffortChipView(deb, rounds, "t1", now, window)
	if chip.Poll {
		t.Fatalf("terminal debate must not poll: %+v", chip)
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/handlers/ -run 'TestBuildDebateView|TestBuildEffortChipView' -p 1 -count=1 -timeout 120s -v`
Expected: FAIL — `undefined: buildDebateView`

- [ ] **Step 3: Implement** — create `internal/handlers/debate_view.go`:

```go
// Package handlers — debate view model (UI refactor spec §6).
//
// Pure projections of (FeatureDebate, []DebateRound) into what the
// workspace templates render. Version labels ("v1, v2…") are DISPLAY-
// ONLY, derived by counting accepted rounds in round_number order; all
// actions key on round IDs / round_number, never on the label, because
// labels renumber after a Restore.

package handlers

import (
	"time"

	"github.com/madalin/forgedesk/internal/models"
)

// VersionEntry is one rail row. Dismissed (rejected) entries are
// context-only: Accepted=false, VersionLabel=0, no View/Restore.
type VersionEntry struct {
	DecidedAt    *time.Time
	RoundID      string
	Provider     string
	Feedback     string // snippet for the rail; empty when none given
	Model        string // staff/superadmin only — zeroed for clients
	RoundNumber  int
	VersionLabel int   // 0 for dismissed entries
	RestoreFrom  int   // undo ?from= value: RoundNumber+1
	CostMicros   int64 // staff/superadmin only
	Accepted     bool
	IsCurrent    bool
}

// SuggestionView is the pending in_review round, if any.
type SuggestionView struct {
	RoundID     string
	Provider    string
	InputText   string
	OutputText  string
	NextVersion int // CurrentVersionLabel + 1
}

// DebateView feeds debate.html and the region partials.
type DebateView struct {
	Pending               *SuggestionView
	CurrentDecidedAt      *time.Time
	CurrentProvider       string // provider of latest accepted round; "" at v0
	LatestAcceptedRoundID string
	Versions              []VersionEntry // newest-first
	CurrentVersionLabel   int
	CanEditSeed           bool
	IsStaff               bool
}

// EffortChipView feeds debate_effort_chip.html.
type EffortChipView struct {
	Debate    *models.FeatureDebate
	TicketID  string
	OOB       bool
	Rescoring bool // latest accepted round isn't the scored one
	Poll      bool // include the hx-trigger self-poll attributes
}

func buildDebateView(deb *models.FeatureDebate, rounds []models.DebateRound, isStaff bool) DebateView {
	v := DebateView{IsStaff: isStaff}

	accepted := 0
	entries := make([]VersionEntry, 0, len(rounds))
	for _, r := range rounds { // rounds are round_number ASC
		switch r.Status {
		case "in_review":
			rr := r
			v.Pending = &SuggestionView{
				RoundID:    rr.ID,
				Provider:   rr.Provider,
				InputText:  rr.InputText,
				OutputText: rr.OutputText,
			}
			continue // never in the rail
		case "accepted":
			accepted++
			v.CurrentVersionLabel = accepted
			v.CurrentProvider = r.Provider
			v.CurrentDecidedAt = r.DecidedAt
			v.LatestAcceptedRoundID = r.ID
		}
		e := VersionEntry{
			RoundID:     r.ID,
			RoundNumber: r.RoundNumber,
			RestoreFrom: r.RoundNumber + 1,
			Provider:    r.Provider,
			Accepted:    r.Status == "accepted",
			DecidedAt:   r.DecidedAt,
		}
		if e.Accepted {
			e.VersionLabel = accepted
		}
		if r.Feedback != nil {
			e.Feedback = *r.Feedback
		}
		if isStaff {
			e.Model = r.Model
			if r.CostMicros != nil {
				e.CostMicros = *r.CostMicros
			}
		}
		entries = append(entries, e)
	}
	// Newest-first for the rail; mark the current version.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	for i := range entries {
		if entries[i].Accepted && entries[i].RoundID == v.LatestAcceptedRoundID {
			entries[i].IsCurrent = true
			break
		}
	}
	v.Versions = entries

	if v.Pending != nil {
		v.Pending.NextVersion = v.CurrentVersionLabel + 1
	}
	v.CanEditSeed = len(rounds) == 0 && deb.InFlightRequestID == nil
	return v
}

// buildEffortChipView decides staleness and polling server-side (spec
// §5.2): poll only while the debate is active AND the latest accept is
// younger than pollWindow (StaleReservationAge, 90s) — after that the
// chip settles without client-side timers, covering permanent scorer
// failure and terminal debates.
func buildEffortChipView(deb *models.FeatureDebate, rounds []models.DebateRound,
	ticketID string, now time.Time, pollWindow time.Duration,
) EffortChipView {
	chip := EffortChipView{Debate: deb, TicketID: ticketID}

	var latest *models.DebateRound
	for i := range rounds {
		if rounds[i].Status == "accepted" {
			latest = &rounds[i]
		}
	}
	if latest == nil {
		return chip // no accepted rounds: empty state, never polls
	}
	chip.Rescoring = deb.LastScoredRoundID == nil || *deb.LastScoredRoundID != latest.ID
	chip.Poll = chip.Rescoring &&
		deb.Status == models.DebateStatusActive &&
		latest.DecidedAt != nil &&
		now.Sub(*latest.DecidedAt) < pollWindow
	return chip
}
```

- [ ] **Step 4: Run tests, verify pass:** same command as Step 2 → PASS. Then `go build ./...` → clean.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/debate_view.go internal/handlers/debate_view_test.go
git commit -m "feat(debate): view model for document-workspace UI"
```

---

### Task 4: New templates + `debate.html` rewrite + `ShowDebate` update

The 7 new component files land and the page is rewritten. **Old components (`debate_seed/round/next_round/sidebar.html`) are NOT deleted yet** — Accept/Reject handlers still render them until Task 9. Client-facing language per spec §4.6; `data-testid` attributes for E2E stability.

**Files:**
- Create: `templates/components/debate_document.html`, `debate_suggestion.html`, `debate_composer.html`, `debate_versions.html`, `debate_effort_chip.html`, `debate_error.html`
- Modify: `templates/pages/debate.html` (full rewrite)
- Modify: `internal/handlers/debate.go` (`ShowDebate` only)
- Test: `internal/handlers/debate_test.go` (update page-markup assertions)

- [ ] **Step 1: Create `templates/components/debate_document.html`**

Data contract: `dict "OOB" bool "TicketID" string "View" DebateView "Debate" *FeatureDebate "Viewing" *ViewingVersion` (Viewing nil for current; the `ViewingVersion` struct arrives in Task 7 — until then handlers pass nil and the `{{if .Viewing}}` branch is dormant).

```html
{{define "debate_document.html"}}
{{if .OOB}}<div id="debate-document" hx-swap-oob="true" class="card bg-base-100 border border-base-300 shadow-sm" data-testid="debate-document">{{else}}<div id="debate-document" class="card bg-base-100 border border-base-300 shadow-sm" data-testid="debate-document">{{end}}
  <div class="card-body p-5 gap-2">
    {{if .Viewing}}
      <div class="alert alert-info py-2 px-3 text-sm flex items-center justify-between" data-testid="debate-viewing-banner">
        <span>Viewing version {{.Viewing.Label}} — read-only</span>
        <span class="flex gap-2">
          <button class="btn btn-ghost btn-xs"
                  hx-get="/tickets/{{.TicketID}}/debate/document"
                  hx-target="#debate-document" hx-swap="outerHTML"
                  data-testid="debate-back-to-current">Back to current</button>
          <form hx-post="/tickets/{{.TicketID}}/debate/undo?from={{.Viewing.RestoreFrom}}"
                hx-target="#debate-stage" hx-swap="innerHTML"
                hx-confirm="Restore version {{.Viewing.Label}}? Versions after this one will be removed.">
            {{csrfField}}
            <button type="submit" class="btn btn-warning btn-xs">Restore</button>
          </form>
        </span>
      </div>
      <div class="prose prose-sm max-w-none">{{markdown .Viewing.Text}}</div>
    {{else}}
      <div class="flex items-center justify-between flex-wrap gap-1">
        <span class="text-xs uppercase tracking-wider text-base-content/60 font-medium">
          {{if eq .View.CurrentVersionLabel 0}}Starting text{{else}}Current description · version {{.View.CurrentVersionLabel}}{{end}}
        </span>
        {{if .View.CurrentProvider}}
          <span class="text-xs text-base-content/50">last change by {{providerLabel .View.CurrentProvider}}{{if .View.CurrentDecidedAt}}, {{relTime .View.CurrentDecidedAt}}{{end}}</span>
        {{end}}
      </div>
      <div class="prose prose-sm max-w-none" data-testid="debate-current-text">{{markdown .Debate.CurrentText}}</div>
      {{if .View.CanEditSeed}}
        <div x-data="{editing: false}" class="mt-1">
          <button type="button" class="btn btn-ghost btn-xs text-base-content/60"
                  x-show="!editing" @click="editing = true" data-testid="debate-edit-seed">
            Edit starting text
          </button>
          <form x-show="editing" x-cloak
                hx-post="/tickets/{{.TicketID}}/debate/seed"
                hx-target="#debate-document" hx-swap="outerHTML"
                class="flex flex-col gap-2">
            {{csrfField}}
            <textarea name="seed" rows="8" class="textarea textarea-bordered w-full text-sm"
                      data-testid="debate-seed-input">{{.Debate.SeedDescription}}</textarea>
            <div class="flex gap-2">
              <button type="submit" class="btn btn-primary btn-xs">Save</button>
              <button type="button" class="btn btn-ghost btn-xs" @click="editing = false">Cancel</button>
            </div>
          </form>
        </div>
      {{end}}
    {{end}}
  </div>
</div>
{{end}}
```

- [ ] **Step 2: Create `templates/components/debate_composer.html`**

Data: `dict "TicketID" string "Providers" []string "Feedback" string`. Keeps the PR #80 first-radio script.

```html
{{define "debate_composer.html"}}
<div class="card bg-base-100 border border-base-300" data-testid="debate-composer">
  <div class="card-body p-5 gap-3">
    <div class="text-xs uppercase tracking-wider text-base-content/60 font-medium">
      Want it improved further?
    </div>
    {{if .Providers}}
      <form id="debate-suggest-form"
            hx-post="/tickets/{{.TicketID}}/debate/rounds"
            hx-target="#debate-stage" hx-swap="innerHTML"
            hx-indicator="#debate-thinking"
            hx-disabled-elt="find button[type='submit']"
            hx-on::before-request="const p = this.querySelector('input[name=provider]:checked'); const t = document.getElementById('debate-thinking-provider'); if (p && t) { t.textContent = p.dataset.label || p.value; }"
            class="flex flex-col gap-3">
        {{csrfField}}
        <textarea name="feedback" rows="2" maxlength="2000"
                  placeholder="Optional: tell the AI what to focus on, e.g. 'add acceptance criteria'"
                  class="textarea textarea-bordered w-full text-sm"
                  data-testid="debate-feedback">{{.Feedback}}</textarea>
        <div role="radiogroup" aria-label="AI provider" class="flex flex-wrap items-center gap-2">
          {{range $p := .Providers}}
            <label class="cursor-pointer" data-provider="{{$p}}">
              <input type="radio" name="provider" value="{{$p}}" data-label="{{providerLabel $p}}" class="peer sr-only">
              <span class="badge badge-soft {{providerBadgeClass $p}} cursor-pointer select-none
                           peer-checked:badge-outline peer-checked:ring-2 peer-checked:ring-offset-2
                           peer-checked:ring-offset-base-100
                           peer-focus-visible:ring-2 peer-focus-visible:ring-primary transition-all">
                {{providerLabel $p}}
              </span>
            </label>
          {{end}}
        </div>
        <script>
          (function () {
            var first = document.querySelector('#debate-suggest-form input[name="provider"]');
            if (first) { first.checked = true; }
          })();
        </script>
        <div>
          <button type="submit" class="btn btn-primary btn-sm" data-testid="debate-suggest">
            Suggest improvements
          </button>
        </div>
      </form>
    {{else}}
      <div class="text-sm text-base-content/60">
        No AI providers are configured on this server — suggestions are unavailable.
      </div>
    {{end}}
    <div id="debate-thinking" class="htmx-indicator border-2 border-dashed border-primary/40 rounded-lg p-4 text-center text-sm" aria-live="polite">
      <span class="loading loading-spinner loading-sm text-primary"></span>
      <strong id="debate-thinking-provider">The AI</strong> is writing a suggestion…
      <div class="text-xs text-base-content/50 mt-1">usually takes about 10 seconds</div>
    </div>
    <div class="text-xs text-base-content/50">
      Happy with it as-is? Use <strong>✓ Use this version</strong> in the header — it saves the text to the ticket.
    </div>
  </div>
</div>
{{end}}
```

- [ ] **Step 3: Create `templates/components/debate_suggestion.html`**

Data: `dict "TicketID" string "Pending" *SuggestionView`.

```html
{{define "debate_suggestion.html"}}
<div class="card bg-base-100 border-2 border-primary overflow-hidden" data-testid="debate-suggestion"
     x-data="{tab: 'preview'}">
  <div class="flex items-center justify-between px-4 py-2 bg-primary/10">
    <span class="text-sm"><strong>{{providerLabel .Pending.Provider}}</strong> suggests an improvement</span>
    <span class="text-xs text-primary">would become version {{.Pending.NextVersion}}</span>
  </div>
  <div class="px-4">
    <div role="tablist" class="tabs tabs-border">
      <button type="button" role="tab" class="tab" :class="tab === 'preview' && 'tab-active'"
              @click="tab = 'preview'" data-testid="debate-tab-preview">Preview</button>
      <button type="button" role="tab" class="tab" :class="tab === 'changes' && 'tab-active'"
              @click="tab = 'changes'" data-testid="debate-tab-changes">What changed</button>
    </div>
    <div x-show="tab === 'preview'" class="prose prose-sm max-w-none py-3" data-testid="debate-preview-pane">
      {{markdown .Pending.OutputText}}
    </div>
    <div x-show="tab === 'changes'" x-cloak class="py-3 text-sm" data-testid="debate-changes-pane">
      {{renderInlineDiff .Pending.InputText .Pending.OutputText}}
    </div>
  </div>
  <div class="flex gap-2 px-4 py-3 border-t border-base-300 bg-base-100">
    <button class="btn btn-success btn-sm" data-testid="debate-accept"
            hx-post="/tickets/{{.TicketID}}/debate/rounds/{{.Pending.RoundID}}/accept"
            hx-target="#debate-stage" hx-swap="innerHTML">
      Accept — make this version {{.Pending.NextVersion}}
    </button>
    <button class="btn btn-ghost btn-sm" data-testid="debate-dismiss"
            hx-post="/tickets/{{.TicketID}}/debate/rounds/{{.Pending.RoundID}}/reject"
            hx-target="#debate-stage" hx-swap="innerHTML">
      Dismiss
    </button>
  </div>
</div>
{{end}}
```

- [ ] **Step 4: Create `templates/components/debate_versions.html`**

Data: `dict "OOB" bool "TicketID" string "View" DebateView`.

```html
{{define "debate_versions.html"}}
{{if .OOB}}<aside id="debate-versions" hx-swap-oob="true" data-testid="debate-versions">{{else}}<aside id="debate-versions" data-testid="debate-versions">{{end}}
  <details class="card bg-base-100 border border-base-300 shadow-sm"
           x-data x-init="$el.open = window.matchMedia('(min-width: 1024px)').matches">
    <summary class="cursor-pointer px-4 py-3 text-xs uppercase tracking-wider text-base-content/60 font-medium select-none">
      Versions
    </summary>
    <div class="px-3 pb-3 flex flex-col gap-1.5 text-sm">
      {{$tid := .TicketID}}
      {{range .View.Versions}}
        {{if .Accepted}}
          <div class="rounded-md border p-2 {{if .IsCurrent}}bg-success/10 border-success/40{{else}}border-base-300{{end}}"
               data-testid="debate-version-{{.VersionLabel}}">
            <div class="flex items-center justify-between">
              <span><strong>v{{.VersionLabel}}</strong> · {{providerLabel .Provider}}</span>
              <span class="text-xs text-base-content/50">{{relTime .DecidedAt}}</span>
            </div>
            {{if .Feedback}}<div class="text-xs text-base-content/60 italic mt-0.5">"{{truncate .Feedback 80}}"</div>{{end}}
            {{if $.View.IsStaff}}<div class="text-xs text-base-content/40 mt-0.5">{{.Model}} · {{.CostMicros}}µ$</div>{{end}}
            {{if .IsCurrent}}
              <div class="text-xs text-success mt-0.5">current</div>
            {{else}}
              <div class="flex gap-2 mt-1">
                <button class="btn btn-ghost btn-xs text-primary"
                        hx-get="/tickets/{{$tid}}/debate/versions/{{.RoundID}}"
                        hx-target="#debate-document" hx-swap="outerHTML">View</button>
                <form hx-post="/tickets/{{$tid}}/debate/undo?from={{.RestoreFrom}}"
                      hx-target="#debate-stage" hx-swap="innerHTML"
                      hx-confirm="Restore version {{.VersionLabel}}? Versions after this one will be removed.">
                  {{csrfField}}
                  <button type="submit" class="btn btn-ghost btn-xs text-primary" data-testid="debate-restore-{{.VersionLabel}}">Restore</button>
                </form>
              </div>
            {{end}}
          </div>
        {{else}}
          <div class="rounded-md p-2 text-base-content/50">
            <s>{{providerLabel .Provider}} suggestion</s> dismissed
            <span class="text-xs">· {{relTime .DecidedAt}}</span>
          </div>
        {{end}}
      {{end}}
      <div class="rounded-md border border-base-300 p-2">
        <div class="flex items-center justify-between">
          <strong>Original</strong>
          <span class="text-xs text-base-content/50">your starting text</span>
        </div>
        {{if gt .View.CurrentVersionLabel 0}}
          <div class="flex gap-2 mt-1">
            <button class="btn btn-ghost btn-xs text-primary"
                    hx-get="/tickets/{{$tid}}/debate/versions/original"
                    hx-target="#debate-document" hx-swap="outerHTML">View</button>
            <form hx-post="/tickets/{{$tid}}/debate/undo?from=1"
                  hx-target="#debate-stage" hx-swap="innerHTML"
                  hx-confirm="Restore the original text? All versions will be removed.">
              {{csrfField}}
              <button type="submit" class="btn btn-ghost btn-xs text-primary" data-testid="debate-restore-original">Restore</button>
            </form>
          </div>
        {{end}}
      </div>
    </div>
  </details>
</aside>
{{end}}
```

- [ ] **Step 5: Create `templates/components/debate_effort_chip.html`**

Data: `EffortChipView` (struct, not dict — fields: Debate, TicketID, OOB, Rescoring, Poll).

```html
{{define "debate_effort_chip.html"}}
{{if .OOB}}<span id="debate-effort-chip" hx-swap-oob="true" data-testid="debate-effort-chip" {{if .Poll}}hx-get="/tickets/{{.TicketID}}/debate/effort" hx-trigger="load delay:3s" hx-swap="outerHTML"{{end}}>{{else}}<span id="debate-effort-chip" data-testid="debate-effort-chip" {{if .Poll}}hx-get="/tickets/{{.TicketID}}/debate/effort" hx-trigger="load delay:3s" hx-swap="outerHTML"{{end}}>{{end}}
  {{if .Rescoring}}
    <span class="badge badge-ghost gap-1">
      <span class="loading loading-spinner loading-xs"></span> estimating effort…
    </span>
  {{else if .Debate.EffortScore}}
    {{$score := derefInt .Debate.EffortScore}}
    <div class="dropdown dropdown-end">
      <button type="button" tabindex="0" class="badge gap-1 cursor-pointer
        {{if le $score 5}}badge-success badge-soft{{else if le $score 8}}badge-warning badge-soft{{else}}badge-error badge-soft{{end}}">
        Effort <strong>{{$score}}/10</strong>{{if .Debate.EffortHours}} · ~{{derefInt .Debate.EffortHours}}h{{end}} ⓘ
      </button>
      <div tabindex="0" class="dropdown-content card card-sm bg-base-100 border border-base-300 shadow-lg w-72 z-10">
        <div class="card-body p-3 text-xs gap-1">
          <p class="font-medium">
            {{if le $score 5}}Fits in a single feature task.{{else if le $score 8}}Needs sub-tasks from the start.{{else}}Consider splitting into multiple features.{{end}}
          </p>
          {{if .Debate.EffortReasoning}}<p class="text-base-content/70">{{derefStr .Debate.EffortReasoning}}</p>{{end}}
          <p class="text-base-content/50">Estimate assumes a full-stack, mid-senior developer.
            {{if .Debate.EffortScoredAt}}Updated {{relTime .Debate.EffortScoredAt}} · via Gemini{{end}}</p>
        </div>
      </div>
    </div>
  {{else}}
    <span class="badge badge-ghost text-base-content/50">Effort — appears after the first accepted suggestion</span>
  {{end}}
</span>
{{end}}
```

(The `{{if .Poll}}` block sits inside an already-open tag emitting two complete attributes with values — not the PR #80 bare-boolean shape. If `TestShowDebate_*` rendering produces truncated output in Step 9, fall back to the duplicated-open-tag pattern for the Poll variant too.)

- [ ] **Step 6: Create `templates/components/debate_error.html`**

Data: `dict "Message" string`.

```html
{{define "debate_error.html"}}
<div class="alert alert-warning text-sm flex items-center justify-between" data-testid="debate-error" x-data>
  <span>{{.Message}}</span>
  <button type="button" class="btn btn-ghost btn-xs" @click="$el.closest('[data-testid=debate-error]').remove()">✕</button>
</div>
{{end}}
```

- [ ] **Step 7: Rewrite `templates/pages/debate.html`**

```html
{{define "content"}}
{{with .Data}}
<div class="max-w-6xl mx-auto p-6" id="debate-workspace" data-theme="forgedesk">
  <header class="flex items-center justify-between mb-6 pb-4 border-b border-base-300 flex-wrap gap-3">
    <div class="flex flex-col gap-1">
      <a href="/tickets/{{.Ticket.ID}}"
         class="text-sm text-base-content/60 hover:text-base-content inline-flex items-center gap-1 w-fit">
        <span aria-hidden="true">←</span> Back to ticket
      </a>
      <h2 class="text-2xl font-semibold tracking-tight">Refine description — {{.Ticket.Title}}</h2>
    </div>
    {{if .Debate}}
      <div class="flex items-center gap-3 flex-wrap">
        {{template "debate_effort_chip.html" .Chip}}
        <form hx-post="/tickets/{{.Ticket.ID}}/debate/approve"
              hx-confirm="Save this text to the ticket and finish refining?">
          {{csrfField}}
          <button type="submit" class="btn btn-success btn-sm" data-testid="debate-approve"
                  title="Saves this text to the ticket and finishes refining"
                  {{if .View.Pending}}disabled{{end}}>
            ✓ Use this version
          </button>
        </form>
        {{if .View.Pending}}<span class="text-xs text-base-content/50">Accept or dismiss the pending suggestion first</span>{{end}}
        <form hx-post="/tickets/{{.Ticket.ID}}/debate/abandon"
              hx-confirm="Stop refining? The ticket description stays unchanged and this session is archived.">
          {{csrfField}}
          <button type="submit" class="btn btn-ghost btn-sm text-base-content/60" data-testid="debate-abandon">
            Stop refining
          </button>
        </form>
      </div>
    {{end}}
  </header>

  {{if .Debate}}
    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_260px]">
      <div class="flex flex-col gap-4 min-w-0">
        {{template "debate_document.html" dict "OOB" false "TicketID" .Ticket.ID "View" .View "Debate" .Debate "Viewing" nil}}
        <div id="debate-flash"></div>
        <div id="debate-stage">
          {{if .View.Pending}}
            {{template "debate_suggestion.html" dict "TicketID" .Ticket.ID "Pending" .View.Pending}}
          {{else}}
            {{template "debate_composer.html" dict "TicketID" .Ticket.ID "Providers" .Providers "Feedback" ""}}
          {{end}}
        </div>
      </div>
      {{template "debate_versions.html" dict "OOB" false "TicketID" .Ticket.ID "View" .View}}
    </div>
  {{else}}
    <div class="card bg-base-100 border border-base-300 shadow-sm mt-6">
      <div class="card-body gap-4">
        <h3 class="text-xl font-semibold">Refine this description with AI</h3>
        <p class="text-sm text-base-content/70">
          AI suggestions improve the description one version at a time — you accept or
          dismiss each one. While refining, the ticket description is locked; finish
          or stop refining to edit it directly.
        </p>
        <div class="card bg-base-200 border border-base-300 p-4 prose prose-sm max-w-none">
          {{markdown .Ticket.DescriptionMarkdown}}
        </div>
        <form method="POST" action="/tickets/{{.Ticket.ID}}/debate/start">
          {{csrfField}}
          <button type="submit" class="btn btn-primary" data-testid="debate-start">Refine with AI</button>
        </form>
      </div>
    </div>
  {{end}}
</div>
{{end}}
{{end}}
```

- [ ] **Step 8: Update `ShowDebate`** in `internal/handlers/debate.go` — replace the `Data:` map of the `h.engine.Render(...)` call (and the `Title:` line) with:

```go
	view := buildDebateView(deb, rounds, auth.IsStaffOrAbove(dctx.user.Role))
	chip := buildEffortChipView(deb, rounds, dctx.ticket.ID, time.Now(), h.cfg.StaleReservationAge)

	_ = h.engine.Render(w, r, "debate.html", render.PageData{
		Title:         "Refine — " + dctx.ticket.Title,
		User:          dctx.user,
		Org:           dctx.org,
		Orgs:          middleware.GetOrgs(r),
		Projects:      middleware.GetProjects(r),
		ActiveProject: proj,
		ProjectID:     dctx.ticket.ProjectID,
		CurrentPath:   r.URL.Path,
		Data: map[string]any{
			"Ticket":    dctx.ticket,
			"Org":       dctx.org,
			"User":      dctx.user,
			"Debate":    deb,
			"Rounds":    rounds,
			"View":      view,
			"Chip":      chip,
			"Providers": h.providerNames(),
			"IsStaff":   auth.IsStaffOrAbove(dctx.user.Role),
		},
	})
```

`buildDebateView`/`buildEffortChipView` take a nil-safe `deb`? No — guard: when `deb == nil` (empty state) skip both and pass `"View": DebateView{}, "Chip": EffortChipView{}`; the template only uses them inside `{{if .Debate}}`. Concretely:

```go
	var view DebateView
	var chip EffortChipView
	if deb != nil {
		view = buildDebateView(deb, rounds, auth.IsStaffOrAbove(dctx.user.Role))
		chip = buildEffortChipView(deb, rounds, dctx.ticket.ID, time.Now(), h.cfg.StaleReservationAge)
	}
```

- [ ] **Step 9: Run the handler tests; update markup assertions**

Run: `go test ./internal/handlers/ -run 'TestShowDebate|TestStartDebate|TestDebate_' -p 1 -count=1 -timeout 120s -v`

`TestShowDebate_NoActiveReturnsEmptyState` / `TestShowDebate_ActiveRendersProviderPicker` assert old copy. Update the assertions to the new markers: empty state → `Refine with AI`; active → `data-testid="debate-composer"` and one provider label (e.g. `Claude`). Keep the test *intent* identical (no debate row created on GET; provider picker renders).

- [ ] **Step 10: Verify the full package builds and tests pass**

Run: `go build ./... && go test ./internal/handlers/ -p 1 -count=1 -timeout 120s`
Expected: PASS (Accept/Reject still render old partials — old component files still exist).

- [ ] **Step 11: Commit**

```bash
git add templates/ internal/handlers/
git commit -m "feat(debate): document-workspace templates + ShowDebate view model"
```

---

### Task 5: Error banners — handler helper + role-aware copy + `init.js` listener

**Files:**
- Modify: `internal/handlers/debate.go` (new `renderDebateError`, rewire `writeReservationError`/`writeInsertError`/`writeAcceptError` + inline `http.Error` calls on POST paths)
- Modify: `static/js/app/init.js`
- Test: `internal/handlers/debate_test.go`

- [ ] **Step 1: Write the failing test** — append to `internal/handlers/debate_test.go`:

```go
// Spec §5.4 — HTMX requests get a debate_error.html banner body with
// the error status; the status-code discipline itself is pinned by the
// older tests. Accepting an already-decided round is the stale-tab case.
func TestStaleRoundAction_RendersErrorBanner409(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	rounds := startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{"proposal one"})

	accept := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost,
			"/tickets/"+ticket.ID+"/debate/rounds/"+rounds[0].ID+"/accept", nil)
		req.AddCookie(cookie)
		req.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	first := accept()
	// 204 until Task 9 switches accept to partial responses; both are
	// success here. Task 9 Step 6 tightens this to == http.StatusOK.
	if first.Code >= 400 {
		t.Fatalf("first accept: got %d, want success", first.Code)
	}
	second := accept()
	if second.Code != http.StatusConflict {
		t.Fatalf("second accept: got %d, want 409", second.Code)
	}
	if !strings.Contains(second.Body.String(), `data-testid="debate-error"`) {
		t.Fatalf("409 body must be the error banner partial, got: %s", second.Body.String())
	}
	if second.Header().Get("HX-Retarget") != "#debate-flash" {
		t.Fatalf("HX-Retarget = %q, want #debate-flash", second.Header().Get("HX-Retarget"))
	}
}
```


- [ ] **Step 2: Run, verify FAIL** (no banner body, no HX-Retarget):

`go test ./internal/handlers/ -run TestStaleRoundAction -p 1 -count=1 -timeout 120s -v` → FAIL

- [ ] **Step 3: Implement the helper** in `internal/handlers/debate.go`:

```go
// renderDebateError keeps spec §3.3's status-code discipline but gives
// HTMX requests a human-readable banner (spec UI refactor §5.4). The
// htmx:beforeSwap listener in init.js permits the swap; HX-Retarget
// puts the banner in #debate-flash so the composer/suggestion in
// #debate-stage is never destroyed by an error.
func (h *DebateHandler) renderDebateError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.Header.Get("HX-Request") != "true" {
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("HX-Retarget", "#debate-flash")
	w.Header().Set("HX-Reswap", "innerHTML")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.engine.RenderPartial(w, "debate_error.html", map[string]any{"Message": msg})
}
```

- [ ] **Step 4: Rewire the POST-path errors.** Change the three mappers to take `(w, r, isStaff)` and route through `renderDebateError` with client-language copy; update every call site (`CreateRound`, `AcceptRound`, `RejectRound`, `UndoRound`, `ApproveDebate`, `AbandonDebate` — the inline `http.Error` calls included):

```go
func (h *DebateHandler) writeReservationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, models.ErrDebateNotActive):
		h.renderDebateError(w, r, http.StatusConflict, "This page is out of date — reload to see the latest state.")
	case errors.Is(err, models.ErrInFlightAIRequest):
		h.renderDebateError(w, r, http.StatusConflict, "A suggestion is already being written — give it a few seconds.")
	default:
		h.renderDebateError(w, r, http.StatusInternalServerError, "Something went wrong on our side — nothing was changed.")
	}
}
```

Same treatment for `writeInsertError` (stale-input 409 → "The description changed while the AI was writing — nothing was saved, please try again."; in-review-exists 409 → "A suggestion is already waiting — accept or dismiss it first.") and `writeAcceptError` (not-found 404 → "That suggestion no longer exists — reload the page."; not-in-review 409 → "This page is out of date — reload to see the latest state."). The cap errors in `CreateRound` become role-aware:

```go
		if roundCount, rcErr := h.db.CountActiveRoundsForDebate(r.Context(), deb.ID); rcErr == nil && roundCount >= h.cfg.ClientRoundCap {
			h.renderDebateError(w, r, http.StatusTooManyRequests,
				"You've reached the suggestion limit for this feature — ask us if you need more.")
			return
		}
		if dailyCount, dcErr := h.db.CountUserRoundsLast24h(r.Context(), dctx.user.ID); dcErr == nil && dailyCount >= h.cfg.ClientDailyRoundCap {
			h.renderDebateError(w, r, http.StatusTooManyRequests,
				"You've reached the daily suggestion limit — try again tomorrow or ask us if you need more.")
			return
		}
```

(Caps only apply to clients — the numbers stay out of the copy per spec §5.4. The 502 refiner failures: `h.renderDebateError(w, r, http.StatusBadGateway, providerLabelGo(refiner.Name())+" couldn't produce a suggestion. Nothing was changed — try again.")` — add a tiny package-level `providerLabelGo(name string) string` mirroring the FuncMap helper, or export the existing one; do whichever `render.go` makes easier without an import cycle: a 6-line switch in `debate.go` is fine and avoids the cycle.)

- [ ] **Step 5: Add the `htmx:beforeSwap` listener** — append to `static/js/app/init.js`:

```js
// Debate workspace: allow 4xx/5xx responses to swap so the
// debate_error.html banner (HX-Retarget: #debate-flash) is visible.
// Scoped to the debate page; everywhere else error responses keep
// htmx's default no-swap behavior.
document.addEventListener('htmx:beforeSwap', function (event) {
    if (event.detail.xhr && event.detail.xhr.status >= 400 &&
        event.detail.elt && event.detail.elt.closest &&
        event.detail.elt.closest('#debate-workspace')) {
        event.detail.shouldSwap = true;
        event.detail.isError = false;
    }
});
```

- [ ] **Step 6: Rebuild the JS bundle:** `sh scripts/bundle.sh` → regenerates `static/js/bundle.js`.

- [ ] **Step 7: Run the full handler suite** (the old tests assert error *status codes*, which are unchanged; tests that assert `http.Error` plain-text bodies on HTMX-free requests still pass because non-HTMX falls back to `http.Error`):

`go test ./internal/handlers/ -p 1 -count=1 -timeout 120s` → PASS (fix any body-assertion fallout by adding `HX-Request` awareness to the assertion, never by weakening the status check).

- [ ] **Step 8: Commit**

```bash
git add internal/handlers/ static/js/
git commit -m "feat(debate): human-readable error banners with status-code discipline"
```

---

### Task 6: `GET /debate/effort` — self-polling chip endpoint

**Files:**
- Modify: `internal/handlers/debate.go` (new `EffortChip` handler)
- Modify: `cmd/server/main.go:292` block (route)
- Test: `internal/handlers/debate_test.go`

- [ ] **Step 1: Write the failing tests:**

```go
func TestEffortChipPartial_PollsWhileStale_SettlesAfter90s(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	rounds := startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{"text one"})

	// Accept the round but wipe the scorer result so the chip is stale.
	acceptRoundViaHTTP(t, r, cookie, ticket.ID, rounds[0].ID) // helper below
	_, err := db.Pool.Exec(context.Background(),
		`UPDATE feature_debates SET last_scored_round_id = NULL WHERE ticket_id = $1`, ticket.ID)
	if err != nil { t.Fatal(err) }

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID+"/debate/effort", nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK { t.Fatalf("chip GET: %d", w.Code) }
		return w.Body.String()
	}

	// Fresh accept (decided_at = now) → polling attributes present.
	if body := get(); !strings.Contains(body, `hx-trigger="load delay:3s"`) {
		t.Fatalf("stale+recent chip must poll, got: %s", body)
	}

	// Age the accept past the 90s window → polling attributes absent.
	_, err = db.Pool.Exec(context.Background(),
		`UPDATE feature_debate_rounds SET decided_at = now() - interval '2 minutes'
		 WHERE id = $1`, rounds[0].ID)
	if err != nil { t.Fatal(err) }
	if body := get(); strings.Contains(body, "hx-trigger") {
		t.Fatalf("stale+old chip must NOT poll, got: %s", body)
	}
}

func TestEffortChipPartial_NoPollingWhenDebateTerminal(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	rounds := startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{"text one"})
	acceptRoundViaHTTP(t, r, cookie, ticket.ID, rounds[0].ID)

	_, err := db.Pool.Exec(context.Background(),
		`UPDATE feature_debates SET status = 'abandoned', last_scored_round_id = NULL
		 WHERE ticket_id = $1`, ticket.ID)
	if err != nil { t.Fatal(err) }

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID+"/debate/effort", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("terminal chip GET: %d, want 200 (static chip)", w.Code)
	}
	if strings.Contains(w.Body.String(), "hx-trigger") {
		t.Fatalf("terminal debate must not poll: %s", w.Body.String())
	}
}
```

Add the shared helper once (near `startAndCreateRounds`):

```go
func acceptRoundViaHTTP(t *testing.T, r *chi.Mux, cookie *http.Cookie, ticketID, roundID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticketID+"/debate/rounds/"+roundID+"/accept", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code >= 400 {
		t.Fatalf("accept failed: %d %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Run, verify FAIL** (404 — route missing).

- [ ] **Step 3: Implement the handler** in `debate.go`:

```go
// ── GET /tickets/{ticketID}/debate/effort ─────────────────────────

// EffortChip returns the effort-chip partial. The server decides the
// polling behavior per response (spec UI refactor §5.2): hx-trigger is
// included only while the debate is active and the latest accept is
// younger than StaleReservationAge. Terminal or missing debates render
// a static chip — never an error, the chip is informational.
func (h *DebateHandler) EffortChip(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}

	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Approved/abandoned in another tab: static empty chip, no poll.
		_ = h.engine.RenderPartial(w, "debate_effort_chip.html",
			EffortChipView{Debate: &models.FeatureDebate{}, TicketID: dctx.ticket.ID})
		return
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, "Something went wrong on our side.")
		return
	}
	rounds, err := h.db.GetDebateRounds(r.Context(), deb.ID)
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, "Something went wrong on our side.")
		return
	}
	chip := buildEffortChipView(deb, rounds, dctx.ticket.ID, time.Now(), h.cfg.StaleReservationAge)
	_ = h.engine.RenderPartial(w, "debate_effort_chip.html", chip)
}
```

Wait — `TestEffortChipPartial_NoPollingWhenDebateTerminal` sets the debate to `abandoned`, so `GetActiveDebate` returns `ErrNoRows` and the empty-chip branch serves it; the assertion (no `hx-trigger`) holds. The `buildEffortChipView` terminal guard additionally covers any future query that returns terminal rows.

- [ ] **Step 4: Register the route** in `cmd/server/main.go` after line 292's GET:

```go
		r.Get("/tickets/{ticketID}/debate/effort", debateH.EffortChip)
```

- [ ] **Step 5: Run, verify PASS:** `go test ./internal/handlers/ -run TestEffortChip -p 1 -count=1 -timeout 120s -v`

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/ cmd/server/
git commit -m "feat(debate): self-polling effort chip endpoint"
```

---

### Task 7: `GET /debate/document` + `GET /debate/versions/{roundID}`

**Files:**
- Modify: `internal/handlers/debate.go` (handlers + `ViewingVersion` type)
- Modify: `internal/handlers/debate_view.go` (add `ViewingVersion`)
- Modify: `cmd/server/main.go` (2 routes)
- Test: `internal/handlers/debate_test.go`

- [ ] **Step 1: Add `ViewingVersion`** to `debate_view.go`:

```go
// ViewingVersion drives debate_document.html's read-only older-version
// mode ("Viewing version N — Back to current · Restore").
type ViewingVersion struct {
	Label       int    // 0 means "Original"
	Text        string // the version's full text (output_text or seed)
	RestoreFrom int    // undo ?from= that restores this version
}
```

- [ ] **Step 2: Write the failing tests:**

```go
func TestDocumentPartial_RendersCurrentText(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{}) // start only, no rounds

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID+"/debate/document", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("document GET: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `data-testid="debate-current-text"`) {
		t.Fatalf("expected current-text document partial, got: %s", w.Body.String())
	}
}

func TestVersionViewPartial_AcceptedDismissedAndForeign(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	rounds := startAndCreateRounds(t, r, db, cookie, ticket.ID,
		[]string{"version one text", "to be dismissed"})
	acceptRoundViaHTTP(t, r, cookie, ticket.ID, rounds[0].ID)
	// reject round 2
	req := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/rounds/"+rounds[1].ID+"/reject", nil)
	req.AddCookie(cookie)
	r.ServeHTTP(httptest.NewRecorder(), req)

	get := func(rid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet,
			"/tickets/"+ticket.ID+"/debate/versions/"+rid, nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := get(rounds[0].ID); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "version one text") ||
		!strings.Contains(w.Body.String(), `data-testid="debate-viewing-banner"`) {
		t.Fatalf("accepted version view wrong: %d %s", w.Code, w.Body.String())
	}
	if w := get(rounds[1].ID); w.Code != http.StatusNotFound {
		t.Fatalf("dismissed round view: got %d, want 404", w.Code)
	}
	if w := get("00000000-0000-0000-0000-000000000000"); w.Code != http.StatusNotFound {
		t.Fatalf("foreign round view: got %d, want 404", w.Code)
	}
	if w := get("original"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "Viewing the original") {
		t.Fatalf("original view wrong: %d %s", w.Code, w.Body.String())
	}
}
```

(Tenant isolation comes free from `requireDebateContext` and is already pinned by `TestDebate_RejectsNonFeatureTicket`-era tests; the foreign-UUID case here covers the rounds-of-another-debate hole.)

- [ ] **Step 3: Run, verify FAIL** (404 route-not-found on /document; then handler-level).

- [ ] **Step 4: Implement** in `debate.go`:

```go
// ── GET /tickets/{ticketID}/debate/document ───────────────────────

// ShowDocument returns the #debate-document partial in current mode —
// the "Back to current" target when viewing an older version.
func (h *DebateHandler) ShowDocument(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}
	deb, rounds, ok := h.loadActiveDebateAndRounds(w, r, dctx)
	if !ok {
		return
	}
	view := buildDebateView(deb, rounds, auth.IsStaffOrAbove(dctx.user.Role))
	_ = h.engine.RenderPartial(w, "debate_document.html", map[string]any{
		"OOB": false, "TicketID": dctx.ticket.ID, "View": view, "Debate": deb, "Viewing": nil,
	})
}

// ── GET /tickets/{ticketID}/debate/versions/{roundID} ─────────────

// ShowVersion renders an older ACCEPTED version (or "original" for the
// seed) read-only into #debate-document. Dismissed or unknown rounds
// are 404 — dismissed suggestions never became a version.
func (h *DebateHandler) ShowVersion(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}
	deb, rounds, ok := h.loadActiveDebateAndRounds(w, r, dctx)
	if !ok {
		return
	}
	view := buildDebateView(deb, rounds, auth.IsStaffOrAbove(dctx.user.Role))

	roundID := chi.URLParam(r, "roundID")
	var viewing *ViewingVersion
	if roundID == "original" {
		viewing = &ViewingVersion{Label: 0, Text: deb.SeedDescription, RestoreFrom: 1}
	} else {
		label := 0
		for _, rd := range rounds { // ASC — count accepted to derive the label
			if rd.Status == "accepted" {
				label++
				if rd.ID == roundID {
					viewing = &ViewingVersion{Label: label, Text: rd.OutputText, RestoreFrom: rd.RoundNumber + 1}
					break
				}
			}
		}
	}
	if viewing == nil {
		h.renderDebateError(w, r, http.StatusNotFound, "That version no longer exists — reload the page.")
		return
	}
	_ = h.engine.RenderPartial(w, "debate_document.html", map[string]any{
		"OOB": false, "TicketID": dctx.ticket.ID, "View": view, "Debate": deb, "Viewing": viewing,
	})
}

// loadActiveDebateAndRounds wraps the shared GET-partial preamble: 409
// banner when the debate is no longer active (stale tab), 500 banner
// on infra errors. ok=false means a response was already written.
func (h *DebateHandler) loadActiveDebateAndRounds(w http.ResponseWriter, r *http.Request, dctx debateContext) (*models.FeatureDebate, []models.DebateRound, bool) {
	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.renderDebateError(w, r, http.StatusConflict, "This page is out of date — reload to see the latest state.")
		return nil, nil, false
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, "Something went wrong on our side.")
		return nil, nil, false
	}
	rounds, err := h.db.GetDebateRounds(r.Context(), deb.ID)
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, "Something went wrong on our side.")
		return nil, nil, false
	}
	return deb, rounds, true
}
```

Update `debate_document.html`'s Viewing banner for the Original case — change the banner `<span>` to:

```html
        <span>{{if eq .Viewing.Label 0}}Viewing the original starting text — read-only{{else}}Viewing version {{.Viewing.Label}} — read-only{{end}}</span>
```

- [ ] **Step 5: Register routes** in `cmd/server/main.go`:

```go
		r.Get("/tickets/{ticketID}/debate/document", debateH.ShowDocument)
		r.Get("/tickets/{ticketID}/debate/versions/{roundID}", debateH.ShowVersion)
```

- [ ] **Step 6: Run, verify PASS:** `go test ./internal/handlers/ -run 'TestDocumentPartial|TestVersionView' -p 1 -count=1 -timeout 120s -v`

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/ cmd/server/ templates/
git commit -m "feat(debate): document + version-view GET partials"
```

---

### Task 8: `POST /debate/seed` — edit starting text

The v0.2.0 spec listed this endpoint but it was never implemented; the workspace's "Edit starting text" affordance (Task 4) needs it.

**Files:**
- Modify: `internal/models/queries.go` (new `UpdateDebateSeed` + `ErrSeedFrozen` sentinel — put the sentinel next to the existing `ErrDebateNotActive`)
- Modify: `internal/handlers/debate.go` (`EditSeed` handler)
- Modify: `cmd/server/main.go` (route)
- Test: `internal/models/queries_debate_test.go`, `internal/handlers/debate_test.go`

- [ ] **Step 1: Write the failing model test** — append to `internal/models/queries_debate_test.go`, following its existing setup pattern (grep `StartDebate` there for the seed/ticket fixtures it uses):

```go
func TestUpdateDebateSeed_GuardsRoundsAndInFlight(t *testing.T) {
	// Reuse this file's existing debate fixture helper to obtain
	// (db, debate) with zero rounds.
	db, deb := newActiveDebateFixture(t) // match the actual helper name in this file

	if err := db.UpdateDebateSeed(context.Background(), deb.ID, "edited seed"); err != nil {
		t.Fatalf("seed edit with no rounds must succeed: %v", err)
	}
	got, _ := db.GetActiveDebate(context.Background(), deb.TicketID)
	if got.SeedDescription != "edited seed" || got.CurrentText != "edited seed" {
		t.Fatalf("seed edit must update seed_description AND current_text: %+v", got)
	}

	// In-flight reservation blocks the edit.
	_, err := db.Pool.Exec(context.Background(),
		`UPDATE feature_debates SET in_flight_request_id = gen_random_uuid(), in_flight_started_at = now() WHERE id = $1`, deb.ID)
	if err != nil { t.Fatal(err) }
	if err := db.UpdateDebateSeed(context.Background(), deb.ID, "x"); !errors.Is(err, models.ErrInFlightAIRequest) {
		t.Fatalf("want ErrInFlightAIRequest, got %v", err)
	}
	_, _ = db.Pool.Exec(context.Background(),
		`UPDATE feature_debates SET in_flight_request_id = NULL, in_flight_started_at = NULL WHERE id = $1`, deb.ID)

	// Any existing round freezes the seed.
	insertTestRound(t, db, deb.ID, 1, "rejected") // match the file's round-insert helper
	if err := db.UpdateDebateSeed(context.Background(), deb.ID, "y"); !errors.Is(err, models.ErrSeedFrozen) {
		t.Fatalf("want ErrSeedFrozen, got %v", err)
	}
}
```

(The two fixture helpers exist under file-local names — adapt the two call sites to the real names found in that file; the assertions are the contract.)

- [ ] **Step 2: Run, verify FAIL** (`undefined: UpdateDebateSeed`).

- [ ] **Step 3: Implement the query** in `internal/models/queries.go` (next to the other debate queries):

```go
// ErrSeedFrozen — seed edits are only legal while no rounds exist.
var ErrSeedFrozen = errors.New("seed frozen after first round")

// UpdateDebateSeed edits seed_description AND current_text together —
// before round 1 they are by definition equal (spec §4.1/§4.2 v0.2.0).
// Guards, under the debate row lock: status active, no in-flight AI
// reservation, zero rounds of any status.
func (db *DB) UpdateDebateSeed(ctx context.Context, debateID, text string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	var inFlight *string
	var roundCount int
	if err := tx.QueryRow(ctx, `
		SELECT d.status, d.in_flight_request_id,
		       (SELECT count(*) FROM feature_debate_rounds WHERE debate_id = d.id)
		FROM feature_debates d WHERE d.id = $1 FOR UPDATE`, debateID,
	).Scan(&status, &inFlight, &roundCount); err != nil {
		return err
	}
	if status != DebateStatusActive {
		return ErrDebateNotActive
	}
	if inFlight != nil {
		return ErrInFlightAIRequest
	}
	if roundCount > 0 {
		return ErrSeedFrozen
	}
	if _, err := tx.Exec(ctx, `
		UPDATE feature_debates
		SET seed_description = $1, current_text = $1, updated_at = now()
		WHERE id = $2`, text, debateID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
```

- [ ] **Step 4: Run model test, verify PASS.** `go test ./internal/models/ -run TestUpdateDebateSeed -p 1 -count=1 -timeout 120s -v`

- [ ] **Step 5: Handler + route + handler test.** Handler in `debate.go`:

```go
// ── POST /tickets/{ticketID}/debate/seed ──────────────────────────

// EditSeed updates the starting text before any round exists. Returns
// the refreshed #debate-document partial (the form lives inside it).
func (h *DebateHandler) EditSeed(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}
	seed := strings.TrimSpace(r.FormValue("seed"))
	if seed == "" {
		h.renderDebateError(w, r, http.StatusBadRequest, "The starting text can't be empty.")
		return
	}
	if len(seed) > 20000 {
		h.renderDebateError(w, r, http.StatusRequestEntityTooLarge, "The starting text is too long.")
		return
	}
	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.renderDebateError(w, r, http.StatusConflict, "This page is out of date — reload to see the latest state.")
		return
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, "Something went wrong on our side.")
		return
	}
	switch err := h.db.UpdateDebateSeed(r.Context(), deb.ID, seed); {
	case err == nil:
	case errors.Is(err, models.ErrSeedFrozen):
		h.renderDebateError(w, r, http.StatusBadRequest, "The starting text is locked once a suggestion exists.")
		return
	case errors.Is(err, models.ErrInFlightAIRequest):
		h.renderDebateError(w, r, http.StatusConflict, "A suggestion is being written — wait for it before editing.")
		return
	case errors.Is(err, models.ErrDebateNotActive):
		h.renderDebateError(w, r, http.StatusConflict, "This page is out of date — reload to see the latest state.")
		return
	default:
		h.renderDebateError(w, r, http.StatusInternalServerError, "Something went wrong on our side.")
		return
	}
	h.ShowDocument(w, r) // re-render the document partial with the new text
}
```

Route: `r.Post("/tickets/{ticketID}/debate/seed", debateH.EditSeed)`. Handler test:

```go
func TestEditSeed_UpdatesAndFreezesAfterRound(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{}) // start, 0 rounds

	post := func(seed string) *httptest.ResponseRecorder {
		form := url.Values{"seed": {seed}}
		req := httptest.NewRequest(http.MethodPost,
			"/tickets/"+ticket.ID+"/debate/seed", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := post("better seed"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "better seed") {
		t.Fatalf("seed edit: %d %s", w.Code, w.Body.String())
	}
	startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{"round one"})
	if w := post("nope"); w.Code != http.StatusBadRequest {
		t.Fatalf("frozen seed edit: got %d, want 400", w.Code)
	}
}
```

(`startAndCreateRounds` with an empty outputs slice must start-without-rounds — check its body; if it always creates rounds, call the start POST directly like `TestStartDebate_CreatesActiveRowAndRedirects` does.)

- [ ] **Step 6: Run, verify PASS, commit**

```bash
git add internal/models/ internal/handlers/ cmd/server/
git commit -m "feat(debate): seed editing endpoint (pre-round-1 only)"
```

---

### Task 9: Accept/Reject/Undo → partial + OOB swaps; delete the old UI

The flow switch: kill `Hx-Refresh` (accept) and `Hx-Redirect` (undo), return `#debate-stage` content + OOB document/versions/chip. Old templates and the unified-diff render path die here.

**Files:**
- Modify: `internal/handlers/debate.go` (`AcceptRound`, `RejectRound`, `UndoRound`; new `renderWorkspaceUpdate`)
- Delete: `templates/components/debate_seed.html`, `debate_round.html`, `debate_next_round.html`, `debate_sidebar.html`
- Modify: `internal/render/render.go` (delete the `renderDiff` FuncMap entry)
- Modify: `internal/diff/diff.go` + `diff_test.go` (delete `RenderHTML` + its tests; **keep** `ComputeUnified`)
- Test: `internal/handlers/debate_test.go`

- [ ] **Step 1: Write the failing tests:**

```go
func TestAccept_NoHXRefreshHeader_ReturnsComposerAndOOB(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	rounds := startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{"accepted text"})

	req := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/rounds/"+rounds[0].ID+"/accept", nil)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("accept: got %d, want 200", w.Code)
	}
	if w.Header().Get("Hx-Refresh") != "" {
		t.Fatal("Hx-Refresh must be gone (regression on #97 hack removal)")
	}
	body := w.Body.String()
	if !strings.Contains(body, `data-testid="debate-composer"`) {
		t.Fatalf("primary content must be the composer, got: %s", body)
	}
	for _, oob := range []string{
		`id="debate-document" hx-swap-oob="true"`,
		`id="debate-versions" hx-swap-oob="true"`,
		`id="debate-effort-chip" hx-swap-oob="true"`,
	} {
		if !strings.Contains(body, oob) {
			t.Fatalf("missing OOB fragment %q in: %s", oob, body)
		}
	}
	if !strings.Contains(body, "accepted text") {
		t.Fatal("OOB document must carry the new current text")
	}
}

func TestReject_ReturnsComposerWithFeedbackPreserved(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	// startAndCreateRounds posts feedback="" — create one round with feedback:
	rounds := startAndCreateRoundsWithFeedback(t, r, db, cookie, ticket.ID,
		[]string{"proposal"}, "focus on security")

	req := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/rounds/"+rounds[0].ID+"/reject", nil)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("reject: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "focus on security") {
		t.Fatalf("dismiss must preserve the feedback text in the composer: %s", w.Body.String())
	}
}

func TestUndo_ReturnsComposerAndOOBDocument(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	rounds := startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{"v1 text"})
	acceptRoundViaHTTP(t, r, cookie, ticket.ID, rounds[0].ID)

	req := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/undo?from=1", nil)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Hx-Redirect") != "" {
		t.Fatal("undo must no longer redirect")
	}
	if !strings.Contains(w.Body.String(), `id="debate-document" hx-swap-oob="true"`) {
		t.Fatalf("undo must OOB-refresh the document: %s", w.Body.String())
	}
}
```

`startAndCreateRoundsWithFeedback`: copy `startAndCreateRounds` and thread a `feedback` form value through its round-POSTs (5-line variant; place it next to the original).

- [ ] **Step 2: Run, verify FAIL** (accept returns 204 + Hx-Refresh today).

- [ ] **Step 3: Implement `renderWorkspaceUpdate`** in `debate.go`:

```go
// renderWorkspaceUpdate emits the full post-mutation response: primary
// content for #debate-stage (suggestion if one is pending, composer
// otherwise) followed by OOB fragments for the document, versions rail,
// and effort chip. One response, four regions, no reload (spec §5.3).
func (h *DebateHandler) renderWorkspaceUpdate(w http.ResponseWriter, r *http.Request, dctx debateContext, feedback string) {
	deb, rounds, ok := h.loadActiveDebateAndRounds(w, r, dctx)
	if !ok {
		return
	}
	isStaff := auth.IsStaffOrAbove(dctx.user.Role)
	view := buildDebateView(deb, rounds, isStaff)
	chip := buildEffortChipView(deb, rounds, dctx.ticket.ID, time.Now(), h.cfg.StaleReservationAge)
	chip.OOB = true

	if view.Pending != nil {
		_ = h.engine.RenderPartial(w, "debate_suggestion.html", map[string]any{
			"TicketID": dctx.ticket.ID, "Pending": view.Pending,
		})
	} else {
		_ = h.engine.RenderPartial(w, "debate_composer.html", map[string]any{
			"TicketID": dctx.ticket.ID, "Providers": h.providerNames(), "Feedback": feedback,
		})
	}
	_ = h.engine.RenderPartial(w, "debate_document.html", map[string]any{
		"OOB": true, "TicketID": dctx.ticket.ID, "View": view, "Debate": deb, "Viewing": nil,
	})
	_ = h.engine.RenderPartial(w, "debate_versions.html", map[string]any{
		"OOB": true, "TicketID": dctx.ticket.ID, "View": view,
	})
	_ = h.engine.RenderPartial(w, "debate_effort_chip.html", chip)
}
```

- [ ] **Step 4: Rewire the three handlers.**

In `AcceptRound`, replace the `Hx-Refresh` block (and its comment) with:

```go
	h.renderWorkspaceUpdate(w, r, dctx, "")
```

In `RejectRound`, replace the `debate_next_round.html` RenderPartial block with:

```go
	feedback := ""
	if rounds, rErr := h.db.GetDebateRounds(r.Context(), deb.ID); rErr == nil {
		for _, rd := range rounds {
			if rd.ID == roundID && rd.Feedback != nil {
				feedback = *rd.Feedback
			}
		}
	}
	h.renderWorkspaceUpdate(w, r, dctx, feedback)
```

In `UndoRound`, replace the `Hx-Redirect` block with:

```go
	h.renderWorkspaceUpdate(w, r, dctx, "")
```

`CreateRound`'s success response also changes — replace the `debate_round.html` RenderPartial with `h.renderWorkspaceUpdate(w, r, dctx, "")` (the in_review round is now in `view.Pending`, so the suggestion panel comes back as primary content; OOB versions/chip keep the rail honest).

- [ ] **Step 5: Delete the old surface.**

```bash
git rm templates/components/debate_seed.html templates/components/debate_round.html \
       templates/components/debate_next_round.html templates/components/debate_sidebar.html
```

Remove the `"renderDiff"` FuncMap entry from `internal/render/render.go` (and its `diff.RenderHTML` reference); delete `RenderHTML` from `internal/diff/diff.go` and `TestRenderHTML_*` from `diff_test.go`; delete any `render_test.go` test that exercised `renderDiff`. `ComputeUnified` and its tests stay (still persisted to `diff_unified` for audit).

- [ ] **Step 6: Run everything; fix fallout deliberately**

Run: `go build ./... && go test ./internal/... -p 1 -count=1 -timeout 120s`

Expected fallout to fix (update assertions, never delete intent):
- `TestAcceptRound_UpdatesCurrentText` — asserted 204/Hx-Refresh; now expects 200 + body.
- `TestUndoRound_*` — asserted `Hx-Redirect`; now expects 200 + OOB document containing the recomputed text (strengthens the test).
- `TestRejectRound_LeavesCurrentTextUnchanged` — partial name changed; assert on `debate-composer` testid.
- The `TestStaleRoundAction_RendersErrorBanner409` lenient `first.Code >= 400` guard from Task 5 — tighten to `first.Code == http.StatusOK` now.

Also add the spec §8 fire-and-forget pin (the response must not wait on the scorer). `setupDebateTestEnv` wires a fake scorer — find it (`grep -n "scorer" internal/handlers/debate_test.go`) and give it a blocking variant:

```go
// Pins the existing async-scorer behavior through the new response
// shape: accept's 200 + OOB body arrives while the scorer is still
// blocked; the score lands afterwards.
func TestAccept_ReturnsBeforeScorerCompletes(t *testing.T) {
	// Use the test env's fake scorer, swapped for one that blocks on a
	// channel (mirror the fake-refiner pattern already in this file).
	// 1. POST accept with the scorer blocked → expect 200 and the OOB
	//    fragments (same assertions as TestAccept_NoHXRefreshHeader_*).
	// 2. Unblock the scorer; poll feature_debates until
	//    last_scored_round_id equals the accepted round's id (timeout 5s).
	// 3. Assert effort_score is set.
}
```

If the env's scorer isn't swappable per-test, make `setupDebateTestEnv` accept an optional scorer override (variadic param, nil-safe) — a 5-line change that no existing call site needs to touch.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(debate): partial+OOB responses for accept/dismiss/restore; drop Hx-Refresh and the unified-diff UI"
```

---

### Task 10: E2E — rewrite `e2e/tests/11-debate.spec.js`

**Files:**
- Modify: `e2e/tests/11-debate.spec.js`

- [ ] **Step 1: Read the current spec file** — keep its login/2FA + fixture setup (`beforeEach`, helper imports) verbatim; only the page interactions change. The stack runs with `DEBATE_REFINER_MODE=fake` (see `docker-compose.test.yml`), so suggestions return canned output instantly.

- [ ] **Step 2: Replace the scenario bodies** with the workspace flow (selector contract = the `data-testid`s from Task 4):

```js
// Golden path: start → suggest → inspect tabs → accept → chip → dismiss → restore → approve
test('debate golden path through the document workspace', async ({ page }) => {
  // (existing navigation to a feature ticket's debate page)
  await page.getByTestId('debate-start').click();
  await page.waitForLoadState('networkidle');

  // Empty chip + composer visible, no suggestion pending
  await expect(page.getByTestId('debate-composer')).toBeVisible();
  await expect(page.getByTestId('debate-effort-chip')).toContainText('appears after');

  // Ask for a suggestion
  await page.getByTestId('debate-feedback').fill('add security requirements');
  await page.getByTestId('debate-suggest').click();
  await expect(page.getByTestId('debate-suggestion')).toBeVisible();

  // Tabs: Preview renders markdown, What changed renders ins/del
  await page.getByTestId('debate-tab-changes').click();
  await expect(page.getByTestId('debate-changes-pane').locator('ins')).toHaveCount(1);
  await page.getByTestId('debate-tab-preview').click();

  // Approve is disabled while a suggestion is pending
  await expect(page.getByTestId('debate-approve')).toBeDisabled();

  // Accept → composer returns, document + rail update without reload
  await page.getByTestId('debate-accept').click();
  await expect(page.getByTestId('debate-composer')).toBeVisible();
  await expect(page.getByTestId('debate-version-1')).toContainText('current');

  // Second suggestion, dismissed → struck-through entry, feedback preserved
  await page.getByTestId('debate-suggest').click();
  await expect(page.getByTestId('debate-suggestion')).toBeVisible();
  page.once('dialog', d => d.accept()); // none expected on dismiss; safety
  await page.getByTestId('debate-dismiss').click();
  await expect(page.getByTestId('debate-versions')).toContainText('dismissed');

  // Restore original (confirm dialog), document reverts
  page.once('dialog', d => d.accept());
  await page.getByTestId('debate-restore-original').click();
  await expect(page.getByTestId('debate-effort-chip')).toContainText('appears after');

  // One more accepted round, then approve writes back to the ticket
  await page.getByTestId('debate-suggest').click();
  await page.getByTestId('debate-accept').click();
  page.once('dialog', d => d.accept());
  await page.getByTestId('debate-approve').click();
  await page.waitForURL(/\/tickets\//);
});
```

Keep/adapt the file's existing negative-path test(s) (abandon flow, non-feature rejection) with the new selectors (`debate-abandon`, confirm dialog).

**Spec deviation, deliberate:** spec §8 lists an E2E "error banner on a forced 502", but `DEBATE_REFINER_MODE=fake` always succeeds, so a 502 can't be forced through the E2E stack without new test plumbing. The banner path is covered at the handler level (`TestStaleRoundAction_RendersErrorBanner409`, Task 5) — YAGNI on a fake-refiner failure mode.

- [ ] **Step 3: Run the E2E suite**

```bash
docker compose -f docker-compose.test.yml up -d --build
cd e2e && npx playwright test tests/11-debate.spec.js
```
Expected: PASS. HTMX gotcha reminder: after boosted form posts use `waitForLoadState('networkidle')`; the suggestion swap is a plain hx-post (not boost) so `expect(...).toBeVisible()` auto-waits suffice.

- [ ] **Step 4: Commit**

```bash
git add e2e/
git commit -m "test(debate): E2E golden path for the document workspace"
```

---

### Task 11: Final verification

- [ ] **Step 1: Full test suite**

```bash
docker compose -f docker-compose.test.yml up -d postgres && docker compose -f docker-compose.test.yml up migrate
go test ./... -p 1 -count=1 -timeout 120s
```
Expected: PASS across all packages.

- [ ] **Step 2: Lint + assets** (per repo workflow: full-repo lint before push)

```bash
golangci-lint run ./...
bash scripts/css.sh && sh scripts/bundle.sh
git status --porcelain   # commit tw.css / bundle.js if they changed
```

- [ ] **Step 3: Manual smoke** — `source .secrets && DEBATE_REFINER_MODE=fake go run ./cmd/server`, open a feature ticket's debate page, walk the golden path once with human eyes (the whole point of this refactor is how it *feels*).

- [ ] **Step 4: Spec-coverage check before handoff** — every spec §5.3 row has a live UI path; §5.4 messages all reachable; `Hx-Refresh` grep returns nothing under `internal/`:

```bash
grep -rn "Hx-Refresh\|HX-Refresh" internal/ templates/ && echo "FAIL: refresh hack survives" || echo "OK"
grep -rn "debate_round.html\|debate_next_round.html\|debate_sidebar.html\|debate_seed.html" internal/ templates/ && echo "FAIL: dead template refs" || echo "OK"
```

- [ ] **Step 5: Hand back for PR** — per project workflow: push branch, open PR, wait for Codex/Gemini bot reviews (~3 min), hand back to Madalin for merge.
