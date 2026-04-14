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
| `ticket_id` | UUID FK → tickets(id) | **`ON DELETE CASCADE`** — debates die with their ticket |
| `project_id` | UUID FK → projects(id) | **`ON DELETE CASCADE`** — denormalized for query scoping; kept in sync only via the initial insert (tickets don't migrate projects) |
| `org_id` | UUID FK → organizations(id) | **`ON DELETE CASCADE`** — denormalized for tenant scoping |
| `started_by` | UUID FK → users(id) | **`ON DELETE RESTRICT`** — user records aren't deleted while they own audit history; deletion is blocked until the debate is removed or reassigned by superadmin |
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

Unique: `(debate_id, round_number)`. Indexes:

- `(debate_id, round_number DESC)` — chronological listing.
- `(debate_id, status, round_number DESC)` — accelerates the undo recompute path (`WHERE status='accepted' ORDER BY round_number DESC LIMIT 1`) and status-filtered lookups without scanning the full partition.

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
| At most one active debate per ticket | Partial unique index + `INSERT ... ON CONFLICT (ticket_id) WHERE status='active' DO NOTHING RETURNING *` in `StartDebate`; on empty result, re-`SELECT` the existing active row and return it idempotently. Handler-level 409 is reserved for the "ticket not feature" case only. |
| Round numbers monotonic per debate | `UNIQUE (debate_id, round_number)` + handler assigns `max+1` inside the same tx that holds `SELECT ... FOR UPDATE` on the debate row |
| Seed immutable after round 1 | Handler rejects 400 on edit-seed if `len(rounds) > 0` |
| Cascading undo | Tx: `SELECT * FROM feature_debates WHERE id=$1 FOR UPDATE` → `DELETE FROM feature_debate_rounds WHERE debate_id=$1 AND round_number >= $2` → recompute + UPDATE debate |
| Ticket description frozen during active debate | `UpdateTicket` handler calls `IsDebateActive`, returns 409 if true. Handler is the **single write path** to `description_markdown` — any bypass (direct SQL, future import handler, etc.) is a bug and must route through this guard. |
| Per-feature round cap (clients only) | Handler counts rounds inside the tx that holds the debate lock, rejects with 429 if over cap — count is re-read under lock to prevent TOCTOU |
| Feature-only (v1) | Handler-side check on `ticket.type`; DB stub `CHECK(true)` placeholder for future relaxation |
| Debate has exactly one terminal state | Every state-changing tx starts with `SELECT status FROM feature_debates WHERE id=$1 FOR UPDATE` and rejects with 409 if `status != 'active'`. Approve and Abandon cannot both succeed on the same debate. |

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

**Error discipline:** distinct branches — never collapsed. Table:

| Condition | Status | Notes |
|---|---|---|
| Ticket not found / wrong tenant | `404` | Generic body; no enumeration hints |
| Ticket exists but `type != 'feature'` | `400` | "debate is for features only" |
| Unknown provider name in form | `400` | |
| Provider key missing at startup | `503` | Provider button still renders; POST fails loudly |
| Feedback or text over length cap | `413` | |
| Round cap reached (clients) | `429` | Staff/superadmin bypass |
| Debate not in `active` status (approve/abandon/accept/reject/undo/create-round) | `409` | Terminal-state guard |
| In-review round already exists (create-round) | `409` | "accept or reject the current round first" |
| Feature has no active debate (rounds endpoint) | `400` | "no active debate — call /start first" |
| `UpdateTicket` while active debate exists | `409` | "debate in progress — finish or abandon to edit directly" |
| AI call failed (timeout, 5xx, parse error) | `502` | Truncated upstream error surfaced for staff; generic for clients |
| Any other DB error | `500` | Logged at ERROR |

**Concurrency model — debate-level locking.** Every state-changing transaction on a debate (`CreateRound`, `AcceptRound`, `RejectRound`, `UndoRoundsFrom`, `ApproveDebate`, `AbandonDebate`) starts with `SELECT * FROM feature_debates WHERE id=$1 FOR UPDATE` as its first statement. This serializes **all** debate mutations on the parent row, not just on individual rounds — which is what the "one in-review round at a time", "undo cascades cleanly", and "approve ⊕ abandon are mutually exclusive" invariants require. A round-level `FOR UPDATE` alone is insufficient because undo spans multiple rounds and approve/abandon are exclusive debate-level outcomes.

Read-only endpoints (GET debate page, GET round partials) use plain `SELECT` and tolerate momentarily-stale reads.

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

User on feature ticket → clicks "Debate this feature" → `GET /debate` → handler's `StartDebate` runs:

```sql
INSERT INTO feature_debates (ticket_id, project_id, org_id, started_by, status,
                             seed_description, current_text)
VALUES ($1, $2, $3, $4, 'active', $5, $5)
ON CONFLICT (ticket_id) WHERE status = 'active' DO NOTHING
RETURNING *;
```

If `RETURNING` yields a row, this is a fresh debate. If it yields nothing, a concurrent request won the insert — re-`SELECT * FROM feature_debates WHERE ticket_id=$1 AND status='active' LIMIT 1` and return that row idempotently. Handler never raises a 409 for this case; the partial unique index is for integrity, not user-facing error signaling.

Page renders with empty rounds list and seed-editable card.

### 4.2 Round lifecycle

1. User edits seed (optional, round 0 only), clicks an AI button.
2. `POST /rounds` with `provider`, `feedback` form fields.
3. Handler validates: ticket is feature, no in-review round exists, round cap not hit.
4. Handler calls `Refiner.Refine(ctx, {CurrentText: debate.current_text, Feedback, SystemPrompt})` with 60s timeout.
5. On success: insert round row (`status='in_review'`, `diff_unified` cached), increment `project_costs.ai_debate` cost aggregate (non-fatal).
6. Return `debate_round.html` partial; HTMX appends it to `#rounds`.

### 4.3 Accept

1. `POST /rounds/:rid/accept`.
2. Open tx. **First statement: `SELECT status FROM feature_debates WHERE id=$1 FOR UPDATE`.** If `status != 'active'` → commit & return 409.
3. `SELECT status FROM feature_debate_rounds WHERE id=:rid FOR UPDATE` — if round not found (raced with undo) → 404; if status already `accepted`/`rejected` → 409.
4. `UPDATE feature_debate_rounds SET status='accepted', decided_at=now() WHERE id=:rid`.
5. `UPDATE feature_debates SET current_text=round.output_text, updated_at=now() WHERE id=:debate_id`.
6. Commit tx. Release debate lock.
7. Call `Scorer.Score(ctx, newCurrentText)` with 60s timeout (outside the tx — scoring should not hold locks).
8. If scorer succeeds: open a short tx, re-`SELECT ... FOR UPDATE` debate, update `effort_score/hours/reasoning/effort_scored_at`. If debate's status is no longer `active` (e.g. approved/abandoned meanwhile), silently skip the update — the score would be stale anyway.
9. If scorer fails: log at WARN with `{debate_id, round_id, provider, error}`; leave previous effort_* values untouched; accept still succeeded.
10. Return `debate_round.html` (accepted state) + OOB `debate_sidebar.html`.

**Rationale for step 6 commit before scoring:** scoring takes 2–10 seconds. Holding `FOR UPDATE` on the debate row across an AI call would serialize all parallel clicks on the page and could deadlock with reject/undo requests. Releasing the lock before the scorer call is the right tradeoff — the score is informational, not transactional.

### 4.4 Undo

1. User clicks "Undo this round" on accepted round N.
2. `POST /undo?from=N`.
3. Open tx. **First statement: `SELECT status FROM feature_debates WHERE id=:debate_id FOR UPDATE`.** If `status != 'active'` → commit & return 409.
4. `DELETE FROM feature_debate_rounds WHERE debate_id=:debate_id AND round_number >= :N`.
5. Recompute `current_text` by selecting `output_text` from the remaining round with the largest `round_number` **whose `status='accepted'`** — if no accepted rounds remain, fall back to `seed_description`. Rejected rounds are ignored in this selection because they never modified `current_text`.
6. **In the same UPDATE:** reset `effort_score`, `effort_hours`, `effort_reasoning`, `effort_scored_at` to NULL alongside `current_text`. Rescoring happens on the next accept.
7. Commit tx.

The debate-level `FOR UPDATE` in step 3 prevents any concurrent `AcceptRound` from writing to a row this delete is about to remove. A concurrent accept would block waiting for the lock, see the round deleted when it finally acquires the lock, and fail cleanly with "round not found" (404) — the user sees a stale-state error and reloads.
4. Return full `#rounds` re-render + OOB sidebar.

### 4.5 Approve

1. `POST /approve`.
2. Open tx. **First: `SELECT status, current_text FROM feature_debates WHERE id=:debate_id FOR UPDATE`.** If `status != 'active'` → commit & return 409 (another request already approved or abandoned).
3. `UPDATE tickets SET description_markdown = :current_text, updated_at=now() WHERE id=:ticket_id`.
4. `UPDATE feature_debates SET status='approved', approved_text=:current_text, updated_at=now() WHERE id=:debate_id`.
5. Commit tx.
6. Response: `HX-Redirect: /projects/:pid/tickets/:tid`.

Once approved, the debate row is immutable for everything except `updated_at`. `approved_text` is frozen. The `UpdateTicket` handler's `IsDebateActive` guard now returns false, so the ticket's description can be edited directly again.

### 4.6 Abandon

1. `POST /abandon` (guarded by `hx-confirm`).
2. Open tx. **First: `SELECT status FROM feature_debates WHERE id=:debate_id FOR UPDATE`.** If `status != 'active'` → commit & return 409.
3. `UPDATE feature_debates SET status='abandoned', updated_at=now() WHERE id=:debate_id`.
4. Commit tx.

Description untouched. Ticket becomes editable again. Approve and Abandon are mutually exclusive — the second caller always gets 409 because the first caller flipped the status under the shared debate lock.

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

**Tenant & validation:**
- `TestCreateRound_RejectsCrossOrgTicket` — user in org A cannot POST against a feature in org B; body contains no enumeration-revealing text.
- `TestCreateRound_RejectsNonFeatureTicket` — debate on `type='bug'` → 400.

**Round cap & roles:**
- `TestClientRoundCap_EnforcedAt10` — 11th round from client → 429.
- `TestStaffBypassesCap` — same scenario, staff → 11th succeeds.
- `TestUndoFreesCapSlot` — client hits cap at 10, undoes round 10, count drops to 9, next round accepted.

**Scorer resilience:**
- `TestAcceptRound_ScorerFailureStillAccepts` — scorer errors; round still accepts; `effort_*` unchanged.
- `TestAcceptRound_ScorerSucceedsAfterDebateTerminal` — scorer call completes after the debate was abandoned mid-flight; subsequent score-update skips silently (no update applied).

**Undo correctness:**
- `TestUndoRound_CascadesLaterRounds` — accept 3 rounds, undo from 2, assert rounds 2/3 gone and `current_text == round_1.output_text`.
- `TestUndoAllRounds_FallsBackToSeed` — accept 2 rounds, undo from 1, `current_text == seed_description` and all `effort_*` NULL.
- `TestUndoWithMixedAcceptedAndRejected` — round 1 accepted, round 2 rejected, round 3 accepted; undo from 3 — `current_text == round_1.output_text` (rejected round 2 ignored).
- `TestUndo_ClearsEffortFields` — accept a round (effort_* populated), undo it, assert all four `effort_*` columns are NULL.

**Concurrency (all require real Postgres):**
- `TestConcurrentStartDebate_Idempotent` — fire two `StartDebate` goroutines on same ticket; both return the same debate row with `status='active'`; exactly one row exists in DB.
- `TestConcurrentAcceptsOnSameRound` — two goroutines accept same in-review round; exactly one 200, other 409.
- `TestConcurrentAcceptAndUndo_Serialized` — one goroutine accepts round 3 while another undoes from round 2; exactly one completes successfully — if undo wins, accept returns 404 (round deleted); if accept wins, undo sees the newly-accepted round and still cascades it away.
- `TestConcurrentApproveAndAbandon_MutuallyExclusive` — fire both at the same debate; one returns 200 with its terminal status, other returns 409; DB shows a single terminal status (approved XOR abandoned).
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
- E1. Live-API integration test suite behind `//go:build integration_ai`
- E2. Background retry of failed scorer calls for debates where `effort_*` has been NULL for >5 minutes since last accept

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
