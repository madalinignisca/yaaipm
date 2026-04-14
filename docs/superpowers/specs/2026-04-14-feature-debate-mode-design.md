# Feature Debate Mode — Design Spec

**Date:** 2026-04-14
**Target release:** v0.2.0 (minor bump; additive feature + schema migration)
**Status:** Approved design; ready for writing-plans.

## 1. Summary

Feature Debate Mode lets a user refine a feature ticket's description through an iterative, multi-AI refactoring loop. On a feature ticket, the user opens a dedicated debate page where they pick one of three AI providers (Claude, ChatGPT, Gemini) to refactor the current description. The refactor returns as a GitHub-style unified diff the user can Accept, Reject, or Undo. Each accepted round updates a cached "current text" and triggers a complexity rescore via Gemini, shown on a right-side sidebar as a 1–10 effort bar plus an estimated human-hours figure. When the user clicks Approve Final, the approved text overwrites the ticket's `description_markdown` and the debate row transitions to `approved` — the full round history is preserved as an immutable audit trail.

## 2. In scope / Out of scope

**In scope (v1):**
- Debate triggered only from tickets with `type = 'feature'`.
- Three AI providers (Claude, ChatGPT, Gemini). OpenAI key added as prerequisite.
- Persistent debates (survive across sessions).
- Sidebar effort score + hours estimate, rescored on every accepted round by Gemini.
- Per-feature round cap: 10 for clients, uncapped for staff/superadmin.
- Cascading undo (undoing round N drops rounds N+1, N+2, ...).
- Direct description edits locked out while a debate is active.
- One active debate per ticket (older approved/abandoned debates retained as audit trail).
- Cost tracked per round in millionths of USD; rolled into `project_costs` monthly aggregate.

**Out of scope (v1, filed as phase-2 issues):**
- Configurable scorer provider per project (admin-only).
- Per-org monthly $ budget with enforcement.
- Feedback textarea auto-save to localStorage.
- Inline editing of accepted AI output before accept.
- Live-API integration test suite.

**Never in scope:**
- WebSocket / SSE streaming for debate rounds (not worth the complexity for 5–10s calls).
- Cross-tenant debate visibility or comparison.
- Branching debate history (one linear accepted-rounds chain per debate).

## 3. Architecture

Three pieces with clear boundaries, each independently testable.

### 3.1 Data layer (`internal/models/`)

New migration `000032_feature_debates.{up,down}.sql`. Two tables:

**`feature_debates`** — one row per debate (active or archived):

| Column | Type | Notes |
|---|---|---|
| `id` | UUID PK | `gen_random_uuid()` |
| `ticket_id` | UUID FK → tickets | `ON DELETE CASCADE` |
| `project_id` | UUID FK → projects | Denormalized for query scoping |
| `org_id` | UUID FK → organizations | Denormalized for tenant scoping |
| `started_by` | UUID FK → users | Audit: who started the debate |
| `status` | TEXT | `'active' \| 'approved' \| 'abandoned'` |
| `seed_description` | TEXT | Snapshot at start; frozen after round 1 |
| `current_text` | TEXT | Latest accepted text; mutated on accept |
| `effort_score` | INT | 1..10, nullable |
| `effort_hours` | INT | Human-hours estimate, nullable |
| `effort_reasoning` | TEXT | Scorer's short justification |
| `effort_scored_at` | TIMESTAMPTZ | Nullable |
| `approved_text` | TEXT | Set on approve; immutable thereafter |
| `created_at`, `updated_at` | TIMESTAMPTZ | |

Partial unique index: `idx_feature_debates_one_active_per_ticket ON feature_debates (ticket_id) WHERE status = 'active'`.

**`feature_debate_rounds`** — one row per round:

| Column | Type | Notes |
|---|---|---|
| `id` | UUID PK | |
| `debate_id` | UUID FK → feature_debates | `ON DELETE CASCADE` |
| `round_number` | INT | 1-based, monotonic per debate |
| `provider` | TEXT | `'claude' \| 'gemini' \| 'openai'` |
| `model` | TEXT | Specific model ID for audit |
| `triggered_by` | UUID FK → users | |
| `feedback` | TEXT | Optional feedback textarea content |
| `input_text` | TEXT | Text given to the AI |
| `output_text` | TEXT | AI's refactored output |
| `diff_unified` | TEXT | Cached unified diff; nullable recomputes |
| `status` | TEXT | `'in_review' \| 'accepted' \| 'rejected'` |
| `input_tokens`, `output_tokens` | INT | |
| `cost_micros` | BIGINT | Millionths of USD |
| `created_at` | TIMESTAMPTZ | |
| `decided_at` | TIMESTAMPTZ | Nullable; set on accept/reject |

Unique: `(debate_id, round_number)`. Index: `(debate_id, round_number DESC)`.

**Queries added to `internal/models/queries.go`:**

- `StartDebate(ctx, ticket, user) → *FeatureDebate`
- `GetActiveDebate(ctx, ticketID) → *FeatureDebate` (may return `pgx.ErrNoRows`)
- `GetDebateRounds(ctx, debateID) → []DebateRound` ordered by `round_number ASC`
- `GetLatestRound(ctx, debateID) → *DebateRound`
- `InsertDebateRound(ctx, input) → *DebateRound`
- `AcceptRound(ctx, roundID) → error` — updates round + updates debate `current_text` in one tx, `SELECT ... FOR UPDATE` on the round
- `RejectRound(ctx, roundID) → error`
- `UndoRoundsFrom(ctx, debateID, fromRoundNumber) → error` — deletes rounds and recomputes `current_text`
- `UpdateEffortScore(ctx, debateID, score, hours, reasoning) → error`
- `ApproveDebate(ctx, debateID, ticketID) → error` — transaction writes `approved_text`, sets status, and updates `tickets.description_markdown`
- `AbandonDebate(ctx, debateID) → error`
- `IsDebateActive(ctx, ticketID) → bool` — used by `UpdateTicket` guard

**Invariant enforcement:**

| Invariant | Where enforced |
|---|---|
| At most one active debate per ticket | Partial unique index |
| Round numbers monotonic per debate | `UNIQUE (debate_id, round_number)` + handler assigns `max+1` in tx |
| Seed immutable after round 1 | Handler rejects 400 on edit-seed if `len(rounds) > 0` |
| Cascading undo | `DELETE FROM feature_debate_rounds WHERE debate_id = $1 AND round_number >= $2` inside tx |
| Ticket description frozen during active debate | `UpdateTicket` handler calls `IsDebateActive`, returns 409 if true |
| Per-feature round cap (clients only) | Handler counts rounds, rejects with 429 if over cap |
| Feature-only (v1) | Handler-side check on `ticket.type`; DB stub `CHECK(true)` placeholder for future relaxation |

### 3.2 Provider abstraction (`internal/ai/`)

**New interface** (`internal/ai/refiner.go`):

```go
type Refiner interface {
    Name() string                                                          // "claude" | "gemini" | "openai"
    Model() string                                                         // specific model ID
    Refine(ctx context.Context, in RefineInput) (RefineOutput, error)
}

type RefineInput struct {
    CurrentText  string
    Feedback     string
    SystemPrompt string
}

type RefineOutput struct {
    Text  string
    Usage RefineUsage
}

type RefineUsage struct {
    InputTokens, OutputTokens int
    CostMicros                int64
    Model                     string
}

type Scorer interface {
    Score(ctx context.Context, text string) (ScoreResult, error)
}

type ScoreResult struct {
    Score, Hours int
    Reasoning    string
    Usage        RefineUsage
}
```

**Adapters:**

- `internal/ai/anthropic.go` — existing; add `Refine` method (~40 new LOC)
- `internal/ai/gemini_refiner.go` — new (~100 LOC), reuses `*genai.Client` from `gemini.go`
- `internal/ai/gemini_scorer.go` — new (~90 LOC), uses Gemini structured output
- `internal/ai/openai.go` — new (~120 LOC), uses `sashabaranov/go-openai`
- `internal/ai/pricing.go` — new (~60 LOC), per-model $/1k token rates → `cost_micros`
- `internal/ai/prompts/debate_system.md` — embedded system prompt for refiners
- `internal/ai/prompts/debate_score_system.md` — embedded scorer prompt

Missing provider key at startup → Refiner is omitted from the registry; attempting that provider returns 503 (not a silent fallback).

### 3.3 Handler + UI (`internal/handlers/debate.go`, `templates/pages/debate.html`)

**Handler struct:**

```go
type DebateHandler struct {
    db       *models.DB
    engine   *render.Engine
    refiners map[string]ai.Refiner
    scorer   ai.Scorer
    cfg      DebateConfig
}

type DebateConfig struct {
    ClientRoundCap int    // 10
    MaxFeedbackLen int    // 2000
    MaxTextLen     int    // 20000
}
```

**Routes (all under `AuthMiddleware` + `Require2FAVerified`):**

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/:pid/tickets/:tid/debate` | Render debate page; creates row lazily on first visit |
| POST | `/projects/:pid/tickets/:tid/debate/seed` | Edit seed (only while 0 rounds exist) |
| POST | `/projects/:pid/tickets/:tid/debate/rounds` | Create new in-review round |
| POST | `/projects/:pid/tickets/:tid/debate/rounds/:rid/accept` | Accept in-review round + rescore |
| POST | `/projects/:pid/tickets/:tid/debate/rounds/:rid/reject` | Discard in-review round |
| POST | `/projects/:pid/tickets/:tid/debate/undo` | Cascading undo from given round |
| POST | `/projects/:pid/tickets/:tid/debate/approve` | Write approved text to ticket; `HX-Redirect` |
| POST | `/projects/:pid/tickets/:tid/debate/abandon` | Mark debate abandoned |

Every endpoint starts with `requireDebateContext` which validates tenant, fetches the ticket via a new tenant-scoped query `GetTicketForOrg(ctx, ticketID, orgID)`, rejects non-feature tickets with 400.

**Error discipline:** distinct branches for `ErrNotFound (404)`, `ErrNotFeature (400)`, `ErrRoundCap (429)`, `ErrConflict (409)`, provider errors (502), generic (500). Never collapsed.

**Concurrency:** accept/reject/undo run inside a transaction with `SELECT ... FOR UPDATE` on the target round row to serialize parallel clicks.

**UI templates:**

- `templates/pages/debate.html` — full page, composes partials
- `templates/components/debate_seed.html` — top card; editable only while 0 rounds
- `templates/components/debate_round.html` — one round; renders differently per `status`
- `templates/components/debate_sidebar.html` — effort bar + hours + "last updated"
- `templates/components/debate_next_round.html` — feedback textarea + AI buttons + Approve Final

**Alpine.js:** inline only, for collapse/expand of accepted rounds. No new JS file in `static/js/app/`.

**Diff rendering:** server-side in Go via `sergi/go-diff`. New package `internal/diff/` with `ComputeUnified(before, after string) string` and `RenderHTML(unified string) template.HTML`. All output escaped; AI text never rendered as HTML.

**HTMX flow:**

| Action | `hx-*` attrs | Server returns | DOM effect |
|---|---|---|---|
| Click AI button | `hx-post=".../rounds" hx-target="#rounds" hx-swap="beforeend"` | `debate_round.html` (in-review) | New card appended |
| Accept | `hx-post=".../rounds/:rid/accept" hx-target="closest .round-in-review" hx-swap="outerHTML"` | `debate_round.html` (accepted) + OOB sidebar | Card replaced, sidebar updated |
| Reject | Same target, different endpoint | `debate_next_round.html` | In-review card removed; next-round form shown |
| Undo | `hx-post=".../undo?from=:rid" hx-target="#rounds" hx-swap="innerHTML"` | Full timeline partial | Timeline re-rendered |
| Approve | `hx-post=".../approve"` | `HX-Redirect: /tickets/:tid` | Full navigation |

**CSS:** ~80 lines added to `static/css/app.css`. Three-band gradient effort bar (green 1–5, amber 6–8, red 9–10) with a tick-mark pointer.

## 4. Key flows

### 4.1 Start debate

User on feature ticket → clicks "Debate this feature" → `GET /debate` → handler checks for active debate; if none, creates one seeded from `tickets.description_markdown` → page renders with empty rounds list and seed-editable card.

### 4.2 Round lifecycle

1. User edits seed (optional, round 0 only), clicks an AI button.
2. `POST /rounds` with `provider`, `feedback` form fields.
3. Handler validates: ticket is feature, no in-review round exists, round cap not hit.
4. Handler calls `Refiner.Refine(ctx, {CurrentText: debate.current_text, Feedback, SystemPrompt})` with 60s timeout.
5. On success: insert round row (`status='in_review'`, `diff_unified` cached), increment `project_costs.ai_debate` cost aggregate (non-fatal).
6. Return `debate_round.html` partial; HTMX appends it to `#rounds`.

### 4.3 Accept

1. `POST /rounds/:rid/accept`.
2. Tx: `UPDATE round SET status='accepted', decided_at=now()`; `UPDATE debate SET current_text=round.output_text`.
3. Call `Scorer.Score(ctx, newCurrentText)` with 60s timeout.
4. If scorer succeeds: `UPDATE debate SET effort_score, effort_hours, effort_reasoning, effort_scored_at`.
5. If scorer fails: log, leave previous effort_* values, don't block the accept.
6. Return `debate_round.html` (accepted state) + OOB `debate_sidebar.html`.

### 4.4 Undo

1. User clicks "Undo this round" on accepted round N.
2. `POST /undo?from=N`.
3. Tx: delete rounds where `round_number >= N`; recompute `current_text` by selecting the `output_text` of the remaining round with the largest `round_number` **whose `status = 'accepted'`** — if no accepted rounds remain, fall back to `seed_description`. Rejected rounds are ignored in this selection because they never modified `current_text` in the first place. Reset `effort_*` fields to NULL (they will be rescored on the next accept).
4. Return full `#rounds` re-render + OOB sidebar.

### 4.5 Approve

1. `POST /approve`.
2. Tx: `UPDATE tickets SET description_markdown = debate.current_text`; `UPDATE debate SET status='approved', approved_text=current_text`.
3. Response: `HX-Redirect: /projects/:pid/tickets/:tid`.

### 4.6 Abandon

1. `POST /abandon` (guarded by `hx-confirm`).
2. `UPDATE debate SET status='abandoned'`. Description untouched. Ticket becomes editable again (no active debate → `UpdateTicket` guard lifts).

## 5. Security considerations

- **Tenant isolation** — `GetTicketForOrg` enforces `WHERE org_id = $1` at the query level; handler never trusts `pid`/`tid` params alone.
- **CSRF** — existing `filippo.io/csrf/gorilla` middleware applies to all POST endpoints. No new token plumbing.
- **Prompt injection containment** — feedback and seed text wrapped in explicit delimiter blocks in the system prompt; standard defense. Not bulletproof against sophisticated attackers but prevents accidental prompt confusion.
- **XSS on AI output** — output text flows through `goldmark + chroma` (existing sanitized pipeline). Diff rendering escapes all text in `<pre>` blocks. No `.innerHTML` with untrusted content anywhere.
- **Input size caps** — `MaxFeedbackLen = 2000`, `MaxTextLen = 20000`. Prevents abuse of token budget via oversized payloads.
- **No provider fallback** — missing key means the provider's button returns 503; we never silently switch to a different vendor.
- **Cross-tenant enumeration** — 404 returned for any cross-tenant lookup; error body contains no hints about ticket existence or tenant membership.

## 6. Cost & role gating

- Clients, staff, and superadmin can all trigger debates.
- Clients capped at 10 rounds per feature; staff and superadmin bypass.
- Each round increments `project_costs` with category `debate` (monthly aggregate).
- Cost-increment is non-fatal: if it fails, the round still succeeds. The canonical cost data is on `feature_debate_rounds.cost_micros`; `project_costs` is a rollup.
- Phase-2 issues add configurable scorer and per-org monthly $ budget.

## 7. Testing

### 7.1 Unit tests

- `internal/diff/diff_test.go` — `ComputeUnified`, `RenderHTML`, HTML escape checks.
- `internal/ai/pricing_test.go` — per-model rates; unknown model returns 0.
- `internal/ai/refiner_fake_test.go` — fake refiner/scorer contract.

### 7.2 Integration tests (real Postgres, `-p 1` flag)

- `internal/models/queries_debate_test.go` — every query, partial unique index, cascading undo.
- `internal/handlers/debate_test.go` — every endpoint; tenant isolation; distinct error branches; round cap enforcement (10 for clients, bypass for staff); concurrent accepts serialized by `FOR UPDATE`; description-edit lockout while active.

### 7.3 Regression tests (explicit cases)

- `TestCreateRound_RejectsCrossOrgTicket` — user in org A cannot POST against a feature in org B; body contains no enumeration-revealing text.
- `TestCreateRound_RejectsNonFeatureTicket` — debate on `type='bug'` → 400.
- `TestAcceptRound_ScorerFailureStillAccepts` — scorer errors; round still accepts; effort_* unchanged.
- `TestUndoRound_CascadesLaterRounds` — accept 3 rounds, undo from 2, assert rounds 2/3 gone and `current_text == round_1.output_text`.
- `TestClientRoundCap_EnforcedAt10` — 11th round from client → 429.
- `TestStaffBypassesCap` — same scenario, staff → 11th succeeds.
- `TestConcurrentAcceptsOnSameRound` — two goroutines accept; exactly one wins, other 409.
- `TestDescriptionEditLockedDuringActiveDebate` — `UpdateTicket` on feature with active debate → 409.

### 7.4 E2E (Playwright)

- `e2e/tests/06-debate/golden-path.spec.ts` — one test, full happy path. Uses `DEBATE_REFINER_MODE=fake` env var + fake refiner wired in test build. `cmd/server/main.go` panics at startup if this env is set in a non-local `APP_URL`.

### 7.5 Live-API suite (phase-2, build-tagged)

- `internal/ai/live_test.go` gated by `//go:build integration_ai`. Run manually before each release.

## 8. Implementation plan (10 issues, strict order)

| # | Title | Deps | ~LOC |
|---|---|---|---|
| 1 | chore: add `OPENAI_API_KEY` + `OPENAI_MODEL` to cluster Secret + .env template | — | n/a (config) |
| 2 | feat(db): migrations `000032_feature_debates` + queries | — | ~250 |
| 3 | feat(ai): Refiner/Scorer interfaces + pricing table | — | ~150 |
| 4 | feat(ai): AnthropicRefiner adapter | 3 | ~120 |
| 5 | feat(ai): GeminiRefiner + GeminiScorer | 3 | ~200 |
| 6 | feat(ai): OpenAIRefiner adapter | 3, 1 | ~180 |
| 7 | feat(debate): handlers — debate page + start + rounds(create) | 2, 3 | ~300 |
| 8 | feat(debate): accept, reject, undo + concurrency invariant | 7 | ~200 |
| 9 | feat(debate): approve, abandon + description-edit lockout guard | 7, 8 | ~180 |
| 10 | feat(debate): templates, diff rendering, CSS, Playwright E2E | 2–9 | ~500 |

**Phase-2 follow-ups** (filed, not blocking v1):

- A. Per-project configurable scorer provider
- B. Per-org monthly $ budget with enforcement
- C. Feedback textarea auto-save to localStorage
- D. Inline edit of accepted AI output before accept
- E. Live-API integration test suite behind build tag

## 9. New external dependencies

- `github.com/sashabaranov/go-openai` — OpenAI SDK (Apache 2.0)
- `github.com/sergi/go-diff` — diff library (MIT, pure Go, ~1500 LOC, zero deps)

Both added via `go get` during their respective issues. Neither has transitive deps of concern.

## 10. Rollback plan

If the feature must be withdrawn after release, follow this **strict order**:

1. **First:** revert the deployment manifest image pin back to the previous tag and apply. Once the old image is serving all traffic, no code is calling the debate tables or the `IsDebateActive` guard.
2. **Only then:** run `migrate down 1` to drop `feature_debate_rounds` and `feature_debates`. The migration is additive — dropping these tables does not touch any other data.

Reverting in the opposite order (migration first, image second) would leave v0.2.0 pods calling `IsDebateActive` against a missing table and failing every `UpdateTicket` request. The correct ordering avoids the need for defensive table-missing code in the guard.

The only behavioral change to existing flows is the `UpdateTicket` guard itself; with no `feature_debates` table present (pre-migration or post-rollback) and no v0.2.0 code running, behavior is identical to v0.1.0.

## 11. Observability

- Per-round spend visible in `project_costs` under `category = 'debate'`.
- `feature_debates.status` gives active/approved/abandoned counts for reporting.
- Failed Refiner / Scorer calls logged at WARN with provider + model + truncated error; not surfaced to the user as errors (handler returns 502 with generic text for the round endpoint; silent for scorer).

---

*Design approved interactively 2026-04-14. Next step: writing-plans skill generates the detailed implementation plan, then file 10 v1 issues + 5 phase-2 issues on GitHub.*
