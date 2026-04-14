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
| `seed_description` | TEXT | The text the **first AI refactor will operate on**. Editable while no rounds exist AND no AI request is in flight (see `in_flight_request_id` below). Can diverge from `original_ticket_description` if the user edits it before round 1. |
| `original_ticket_description` | TEXT | **Immutable snapshot** of `tickets.description_markdown` at the moment of `StartDebate`. Used by the §4.5 Approve CAS guard to detect external edits made *after* the debate started. Distinct from `seed_description` because the user can edit the seed in the debate UI without it counting as an "external edit." |
| `in_flight_request_id` | UUID | Nullable. Set briefly by `CreateRound` to a generated UUID before releasing the debate lock and starting the AI call; cleared (set NULL) by the same handler when the AI call returns and the round is inserted (or fails). Used to (a) lock seed edits during an in-flight AI request, (b) detect orphaned in-flight requests on operator inspection. |
| `current_text` | TEXT | Latest accepted text; mutated on accept |
| `effort_score` | INT | 1..10, nullable |
| `effort_hours` | INT | Human-hours estimate, nullable |
| `effort_reasoning` | TEXT | Scorer's short justification |
| `effort_scored_at` | TIMESTAMPTZ | Nullable |
| `last_scored_round_id` | UUID FK → feature_debate_rounds(id) | Nullable; **`ON DELETE SET NULL`** (critical — without this, `Undo` deleting the round currently referenced here would fail with a constraint violation). Identifies which accepted round produced the current `effort_*` snapshot. Used to discard stale out-of-order scorer responses (see §4.3 step 8). Because this FK creates a cycle (`feature_debates → feature_debate_rounds → feature_debates`), the migration adds it via `ALTER TABLE` after both tables exist; see §3.1.bis below. |
| `approved_text` | TEXT | Set on approve; immutable thereafter |
| `created_at`, `updated_at` | TIMESTAMPTZ | |

Indexes (`feature_debates`):

- `idx_feature_debates_one_active_per_ticket ON (ticket_id) WHERE status='active'` — partial unique, enforces "one active debate per ticket".
- `idx_feature_debates_ticket ON (ticket_id)` — full index for cross-status lookups (audit history).
- `idx_feature_debates_org_status ON (org_id, status)` — tenant-scoped active-debate listings.
- `idx_feature_debates_project ON (project_id)` — backs the `project_id` FK so cascading project deletes don't full-scan; also speeds project-level audit queries.
- `idx_feature_debates_started_by ON (started_by)` — backs the `started_by` FK so user lookup operations are not blocked by full scans during cascading effects (e.g. user reassignment by superadmin).

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
| `cost_micros` | BIGINT | **Refiner** call cost in millionths of USD (1 cent = 10,000 micros). |
| `scorer_cost_micros` | BIGINT | Nullable. **Scorer** call cost (set on accept when the scorer runs after this round). Without this column, the round-row would be incomplete: scorer cost would only live in `project_costs` rollup and the audit trail couldn't reconstruct per-round AI spend. |
| `created_at` | TIMESTAMPTZ | |
| `decided_at` | TIMESTAMPTZ | Nullable; set on accept/reject |

Unique: `(debate_id, round_number)`. Indexes:

- `(debate_id, round_number DESC)` — chronological listing.
- `(debate_id, status, round_number DESC)` — accelerates the undo recompute path (`WHERE status='accepted' ORDER BY round_number DESC LIMIT 1`) and status-filtered lookups without scanning the full partition.
- `(triggered_by, created_at DESC)` — backs the per-user daily safety fuse query in §6 (`WHERE triggered_by=$1 AND created_at >= now() - INTERVAL '24 hours'`); without this, fuse enforcement does a per-user scan that grows linearly with table size.

**Partial unique index — invariant: at most one in-review round per debate:**

```sql
CREATE UNIQUE INDEX idx_feature_debate_rounds_one_in_review_per_debate
    ON feature_debate_rounds (debate_id) WHERE status = 'in_review';
```

This is the v1 *primary* enforcement of the "one in-review round at a time" rule. Without it, the `CreateRound` handler would have to hold a `FOR UPDATE` lock on the debate row across the 60-second AI call to prevent races — that would serialize Abandon and Undo behind every AI request. With this index, the handler instead runs lock-free during the AI call and lets the INSERT either succeed or fail-with-constraint-violation when the row lands; failure → 409. See §4.2.

### 3.1.bis — Migration ordering for the FK cycle

The `feature_debates.last_scored_round_id` FK references `feature_debate_rounds(id)`, while `feature_debate_rounds.debate_id` references `feature_debates(id)`. To avoid a chicken-and-egg problem at migration time, the up-migration applies the schema in this order:

```sql
-- 1. Create feature_debates without last_scored_round_id.
CREATE TABLE feature_debates ( ... full schema except last_scored_round_id ... );

-- 2. Create feature_debate_rounds (FK to feature_debates already valid).
CREATE TABLE feature_debate_rounds ( ... );

-- 3. Add last_scored_round_id as a nullable column with ON DELETE SET NULL.
ALTER TABLE feature_debates
    ADD COLUMN last_scored_round_id UUID
        REFERENCES feature_debate_rounds(id) ON DELETE SET NULL;

-- 4. Indexes: partial unique on (ticket_id) WHERE active; full ticket_id;
--    (org_id, status); project_id; started_by;
--    rounds: (debate_id, round_number DESC); (debate_id, status, round_number DESC);
--    partial unique (debate_id) WHERE status='in_review';
--    (triggered_by, created_at DESC).
```

**Down-migration must explicitly drop the cyclic FK first** — `DROP TABLE` does not cascade across mutual references. Postgres will error with a dependency violation otherwise:

```sql
-- 1. Break the cycle by dropping the FK that points from debates → rounds.
ALTER TABLE feature_debates DROP CONSTRAINT feature_debates_last_scored_round_id_fkey;

-- 2. Now safe to drop in order.
DROP TABLE feature_debate_rounds;
DROP TABLE feature_debates;
```

### 3.1.ter — `project_costs` constraint update

The existing `project_costs` table (migration `000012`) carries a `CHECK` constraint restricting `category` to `{base_fee, dev_environment, testing_db, testing_container}`. The debate feature needs a new `debate` category for the cost rollup in §6. The same migration adds:

```sql
ALTER TABLE project_costs DROP CONSTRAINT IF EXISTS project_costs_category_check;
ALTER TABLE project_costs ADD CONSTRAINT project_costs_category_check
    CHECK (category IN ('base_fee', 'dev_environment', 'testing_db',
                        'testing_container', 'debate'));
```

**Down-migration is data-dependent.** Restoring the original constraint will fail if any rows with `category='debate'` exist. The down-migration deletes those rows first (the canonical per-round cost data lives on `feature_debate_rounds.cost_micros` + `scorer_cost_micros`, so the rollup data is reconstructable):

```sql
DELETE FROM project_costs WHERE category = 'debate';
ALTER TABLE project_costs DROP CONSTRAINT IF EXISTS project_costs_category_check;
ALTER TABLE project_costs ADD CONSTRAINT project_costs_category_check
    CHECK (category IN ('base_fee', 'dev_environment', 'testing_db',
                        'testing_container'));
```

This makes the down-migration atomic and safe in CI/CD pipelines.

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
- `IsDebateActive(ctx, ticketID) → bool` — used by guarded write paths
- `UpdateTicketMetadata(ctx, ticketID, fields)` — splits off from existing `UpdateTicket`. Updates everything **except** `description_markdown` (priority, dates, title, assigned_to). No debate guard — these fields are independently editable while a debate is active.
- `UpdateTicketDescription(ctx, ticketID, newMarkdown)` — replaces the description-update path of existing `UpdateTicket`. **Embeds `IsDebateActive` guard at the model layer**: returns `ErrDescriptionLocked` if active debate exists. All callers — HTTP handler, AI assistant tool, future import paths — automatically get the guard. This eliminates the single-write-path violation where `internal/handlers/assistant.go`'s `update_ticket` tool could bypass an HTTP-handler-only guard.

The existing `db.UpdateTicket` is removed; `internal/handlers/tickets.go` and `internal/handlers/assistant.go` are updated to call the appropriate split method.

**Invariant enforcement:**

| Invariant | Where enforced |
|---|---|
| At most one active debate per ticket | Partial unique index + `INSERT ... ON CONFLICT (ticket_id) WHERE status='active' DO NOTHING RETURNING *` in `StartDebate`; on empty result, re-`SELECT` the existing active row and return it idempotently. Handler-level 409 is reserved for the "ticket not feature" case only. |
| Round numbers monotonic per debate | `UNIQUE (debate_id, round_number)` + handler assigns `max+1` inside the same tx that holds `SELECT ... FOR UPDATE` on the debate row |
| Seed immutable after round 1 | Handler rejects 400 on edit-seed if `len(rounds) > 0` |
| Cascading undo | Tx: `SELECT * FROM feature_debates WHERE id=$1 FOR UPDATE` → `DELETE FROM feature_debate_rounds WHERE debate_id=$1 AND round_number >= $2` → recompute + UPDATE debate |
| Ticket description frozen during active debate | Guard moved to the **model layer** in `UpdateTicketDescription` — returns `ErrDescriptionLocked` if `IsDebateActive` is true. The HTTP handler translates to 409, the AI assistant tool surfaces the error to the model. The previous handler-only design left a bypass through `internal/handlers/assistant.go`'s `update_ticket` tool which calls the model directly; moving the guard down closes that gap permanently. The CAS check in §4.5 step 3 is the second line of defense for any rolling-deploy or future-code bypass that this guard misses. |
| Per-feature round cap (clients only) | Handler counts rounds inside the tx that holds the debate lock, rejects with 429 if over cap — count is re-read under lock to prevent TOCTOU |
| Feature-only (v1) | Handler-side check on `ticket.type`; DB stub `CHECK(true)` placeholder for future relaxation |
| Debate has exactly one terminal state | Every state-changing tx starts with `SELECT status FROM feature_debates WHERE id=$1 FOR UPDATE` and rejects with 409 if `status != 'active'`. Approve and Abandon cannot both succeed on the same debate. |
| Approve doesn't silently overwrite external edits | `ApproveDebate` performs a compare-and-swap: it reads `tickets.description_markdown` under the same tx and **rejects with 409** if it no longer equals `feature_debates.seed_description`. Any external edit (including from an old v0.1.0 pod during a rolling upgrade window) is therefore detected and the approve fails loudly instead of silently overwriting the edit. The user sees a "description changed externally — review and re-approve" message. |
| Daily per-user round safety fuse | `CreateRound` counts `feature_debate_rounds WHERE triggered_by=$user AND created_at >= now() - INTERVAL '24 hours'`. Clients capped at 50/day, staff/superadmin uncapped. Returns 429 if over. This is **not** the phase-2 per-org $ budget — it's a coarse safety fuse to prevent catastrophic accidental cost spikes (a buggy script, a stuck retry loop) in v1 before budget enforcement ships. Cap value is configurable via `DebateConfig.ClientDailyRoundCap`. |
| Scorer results applied in order (no stale overwrites) | `UpdateEffortScore` UPDATE includes `WHERE id=$1 AND (last_scored_round_id IS NULL OR (SELECT round_number FROM feature_debate_rounds WHERE id = last_scored_round_id) < (SELECT round_number FROM feature_debate_rounds WHERE id = $2))`. Out-of-order scorer responses (where round N's scorer finishes after round N+1's) are silently discarded. The freshest accepted-round score always wins. |

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
    Text         string
    Usage        RefineUsage
    FinishReason string // "stop" | "length" | "content_filter" | "tool_calls" | provider-specific
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

**Output validation contract** — every adapter MUST set `FinishReason` on `RefineOutput`. The handler treats the response as a hard error (502) when:

- `Text` is empty or whitespace-only after trim
- `len(Text) < 10` (sanity floor; a real refactor is never two words)
- `FinishReason == "length"` or `"max_tokens"` — output was truncated by the provider's token limit; accepting it would risk overwriting the ticket description with truncated content

These checks live in the handler, not in the adapter, so the rejection is uniform across all three providers.

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
    ClientRoundCap      int    // 10 — per-feature cap for clients
    ClientDailyRoundCap int    // 50 — per-user-per-day safety fuse for clients
    MaxFeedbackLen      int    // 2000
    MaxTextLen          int    // 20000
    MinOutputLen        int    // 10  — minimum AI output length to accept
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

1. User edits seed (optional, before any round AND before any AI is in flight), clicks an AI button.
2. `POST /rounds` with `provider`, `feedback` form fields.
3. Handler validates outside any tx: ticket is feature, **per-feature round cap not hit (10 for clients), daily per-user safety fuse not hit (50/day for clients)**.
4. **Reservation tx (microseconds):** open tx, `SELECT * FROM feature_debates WHERE id=:debate_id FOR UPDATE`. Validate:
   - `status = 'active'` → else 409.
   - `in_flight_request_id IS NULL` → else 409 ("another AI request is in flight; wait for it").
   - Round caps still satisfied under lock.

   Then `UPDATE feature_debates SET in_flight_request_id = :new_uuid WHERE id = :debate_id`. **Snapshot `current_text`** into a Go variable. Commit & release lock. The reservation lasts until step 7 clears the flag.
5. Handler calls `Refiner.Refine(ctx, {CurrentText: snapshotted_current_text, Feedback, SystemPrompt})` with 60s timeout. **No DB lock held during this call.** Seed-edits and any future "in-flight" CreateRound attempts are blocked because `in_flight_request_id` is set.
6. **Output validation** (per §3.2): reject 502 if `Text` empty, `len(Text) < MinOutputLen`, or `FinishReason in {"length", "max_tokens"}`. On rejection, run a small tx to clear `in_flight_request_id` (release reservation), then return 502.
7. Compute diff (`internal/diff.ComputeUnified(snapshotted_current_text, output)`).
8. **Insert tx (microseconds):** open tx, `SELECT status, current_text FROM feature_debates WHERE id=:debate_id FOR UPDATE`.
   - If `status != 'active'` → clear in_flight_request_id; commit; return 409.
   - **Stale-input check:** if `current_text != snapshotted_current_text` → another operation (Undo, etc.) changed `current_text` while the AI was reasoning. Clear `in_flight_request_id`; commit; return 409 with body `feature description changed while AI was processing — please retry`. The just-generated round would be based on stale input and is discarded.
   - Otherwise, `INSERT INTO feature_debate_rounds (..., status='in_review', round_number=max+1, cost_micros=:refiner_cost)`. The INSERT may still fail with `unique_violation` on `idx_feature_debate_rounds_one_in_review_per_debate` (extremely unlikely given we hold the debate lock and `in_flight_request_id` blocks parallel CreateRounds — but a sibling check defends against any future code path). On conflict → 409.
   - `UPDATE feature_debates SET in_flight_request_id = NULL WHERE id = :debate_id`. Commit, release lock.
9. **Increment `project_costs`** for category=`debate`, amount = `costMicrosToAddCents(refiner_cost_micros)`. Outside the tx, non-fatal. See §6 for the cents/micros conversion.
10. Return `debate_round.html` partial; HTMX appends it to `#rounds`.

**Lock contention**: both lock windows in steps 4 and 8 are microseconds (no AI call inside). Abandon, Undo, and Approve queue briefly behind each other but never wait on AI. The `in_flight_request_id` flag is the cooperative signal that an AI call is pending; it's the cross-tx baton that the lock alone can't carry.

### 4.3 Accept

1. `POST /rounds/:rid/accept`.
2. Open tx. **First statement: `SELECT status FROM feature_debates WHERE id=$1 FOR UPDATE`.** If `status != 'active'` → commit & return 409.
3. `SELECT status FROM feature_debate_rounds WHERE id=:rid FOR UPDATE` — if round not found (raced with undo) → 404; if status already `accepted`/`rejected` → 409.
4. `UPDATE feature_debate_rounds SET status='accepted', decided_at=now() WHERE id=:rid`.
5. `UPDATE feature_debates SET current_text=round.output_text, updated_at=now() WHERE id=:debate_id`.
6. Commit tx. Release debate lock.
7. Call `Scorer.Score(ctx, newCurrentText)` with 60s timeout (outside the tx — scoring should not hold locks). **Capture the just-accepted round's id (call it `acceptedRoundID`) and pass it to step 8.**
8. If scorer succeeds: open a short tx, re-`SELECT ... FOR UPDATE` debate, then update conditionally:
    ```sql
    UPDATE feature_debates
    SET effort_score = $1, effort_hours = $2, effort_reasoning = $3,
        effort_scored_at = now(), last_scored_round_id = :acceptedRoundID
    WHERE id = :debate_id
      AND status IN ('active', 'approved')
      AND (last_scored_round_id IS NULL
           OR (SELECT round_number FROM feature_debate_rounds WHERE id = last_scored_round_id)
              < (SELECT round_number FROM feature_debate_rounds WHERE id = :acceptedRoundID));
    ```
    Two filters apply jointly: (a) `status IN ('active','approved')` allows a late-arriving scorer result to land on an already-approved debate (the audit trail benefits, no risk because approved state is immutable elsewhere). (b) The `last_scored_round_id` ordering check discards out-of-order scorer responses: if a later round was scored first (because its scorer finished sooner), `last_scored_round_id` already points to a round_number ≥ this one's, and the UPDATE matches zero rows. The freshest accepted-round score always wins. Status `'abandoned'` is excluded — score updates on abandoned debates are pure noise.
9. **Persist scorer cost on the round row:** `UPDATE feature_debate_rounds SET scorer_cost_micros = :scorer_cost WHERE id = :acceptedRoundID`. Always done (regardless of whether the score-row update in step 8 matched), so the canonical per-round audit trail in `feature_debate_rounds` is complete.
10. If scorer fails: log at WARN with `{debate_id, round_id, provider, error}`; leave previous effort_* and `scorer_cost_micros` untouched; accept still succeeded.
11. **Increment `project_costs`** (category=`debate`, amount = `costMicrosToAddCents(scorer_cost_micros)`) outside the tx (non-fatal). Both refiner and scorer cost increments are tracked under the same category. The canonical per-round data lives in the round row; `project_costs` is the rollup.
12. Return `debate_round.html` (accepted state) + OOB `debate_sidebar.html`.

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
2. Open tx. **First: `SELECT status, current_text, original_ticket_description FROM feature_debates WHERE id=:debate_id FOR UPDATE`.** If `status != 'active'` → commit & return 409 (another request already approved or abandoned).
3. **Compare-and-swap external-edit guard:** `SELECT description_markdown FROM tickets WHERE id=:ticket_id FOR UPDATE`. If `description_markdown != original_ticket_description` → commit & return 409 with body `description changed externally since debate started — reload and review`. **The CAS compares against `original_ticket_description` (immutable snapshot at debate start), NOT `seed_description`** — the user can edit the seed in the debate UI without that counting as an external edit, but any change to the actual ticket row from outside the debate flow is detected. This catches edits that bypassed the `IsDebateActive` guard (rolling-deploy window where v0.1.0 pods don't have it, direct DB edits, future code paths that forget the guard).
4. `UPDATE tickets SET description_markdown = :current_text, updated_at=now() WHERE id=:ticket_id`.
5. `UPDATE feature_debates SET status='approved', approved_text=:current_text, updated_at=now() WHERE id=:debate_id`.
6. Commit tx.
7. Response: `HX-Redirect: /projects/:pid/tickets/:tid`.

Once approved, the debate row is immutable for everything except `updated_at`. `approved_text` is frozen. The `UpdateTicket` handler's `IsDebateActive` guard now returns false, so the ticket's description can be edited directly again.

The CAS in step 3 means an approve can fail through no fault of the user — if a v0.1.0 pod processed a manual edit during a rolling upgrade window, the debate's approve will reject with 409. The user can then either (a) abandon the debate and start over with the new seed, or (b) reject the external change manually and re-approve. We don't try to auto-merge — divergent edits aren't safely auto-mergeable for prose.

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
- **Per-feature round cap:** clients capped at 10 rounds per feature; staff and superadmin bypass.
- **Per-user daily safety fuse (v1):** clients capped at 50 rounds per 24-hour window across all their debates; staff and superadmin bypass. This is a coarse stop-gap to prevent catastrophic accidental cost spikes (a buggy script, a misclicked retry loop) before the proper per-org $ budget ships in phase-2. Cap value is held in `DebateConfig.ClientDailyRoundCap` and easily tunable. Hitting the daily cap returns 429 with body `daily round limit reached — try again tomorrow or ask staff to bypass`.
- Each round increments `project_costs` with category `debate` (monthly aggregate). **Both** refiner cost (rolled in by §4.2 step 9) and scorer cost (rolled in by §4.3 step 11) flow into the same category.
- **Units conversion is non-trivial** — `feature_debate_rounds.cost_micros` and `scorer_cost_micros` are in **millionths of USD**; `project_costs.amount_cents` is in **cents**. Conversion is `cents = (micros + 9999) / 10000` (ceiling division — under-counting is worse than over-counting by less than a cent for our use case). Implemented as `costMicrosToAddCents(micros int64) int64` helper in `internal/ai/pricing.go` and called by both the refiner and scorer cost-increment paths. Failing to convert (i.e. adding `cost_micros` directly into `amount_cents`) would over-report by 4 orders of magnitude — a $0.05 round becomes a $500 line item.
- Cost-increment is non-fatal: if it fails, the round still succeeds. The canonical per-round cost data is on `feature_debate_rounds.cost_micros` (refiner) + `feature_debate_rounds.scorer_cost_micros` (scorer); `project_costs` is a rollup that can be reconstructed from these two columns.
- Phase-2 issues add configurable scorer and per-org monthly $ budget. The phase-2 budget enforcement supersedes the daily safety fuse but does not replace it (defense in depth — the fuse stays).

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
- `TestApprove_RejectsExternalEdit_CAS` — start debate, accept a round, then bypass the guard with a direct DB UPDATE to `tickets.description_markdown`, then call Approve → 409 with "description changed externally" message; debate stays `active`.
- `TestScorer_StaleResponseDiscarded` — accept rounds N and N+1 in quick succession with a fake scorer that delays N's response artificially; assert that after both responses land, `effort_*` reflects round N+1 (the freshest) and `last_scored_round_id == round_N+1.id`.

**Output validation:**
- `TestRefine_RejectsEmptyOutput` — fake refiner returns `""` → 502, no round inserted.
- `TestRefine_RejectsTruncatedOutput` — fake refiner returns text with `FinishReason="length"` → 502, no round inserted.
- `TestRefine_RejectsTinyOutput` — fake refiner returns `"ok"` (below `MinOutputLen`) → 502.

**Single-write-path enforcement:**
- `TestUpdateTicketDescription_RejectsWhileDebateActive` — call `db.UpdateTicketDescription` directly (no HTTP layer); returns `ErrDescriptionLocked` if a debate is active. Verifies the guard is at the model layer, not just the handler.
- `TestAssistantUpdateTicketTool_RespectsDebateLockout` — Gemini assistant `update_ticket` tool is invoked while a debate is active on the target feature → tool returns the `ErrDescriptionLocked` error to the model, which surfaces it in the chat. No silent overwrite.
- `TestUpdateTicketMetadata_AllowedDuringDebate` — updating priority/dates/title on a feature with an active debate succeeds (only `description_markdown` is locked).

**Cost rollup completeness:**
- `TestCostRollup_IncludesScorerSpend` — accept a round with fake refiner (cost_micros=100,000) and fake scorer (cost_micros=50,000); assert `project_costs` row for category=`debate` increased by 15 cents (= 150,000 micros / 10,000), not 150,000 cents.
- `TestCostMicrosToAddCents` — unit test for the conversion helper: 0 → 0, 1 → 1, 9999 → 1, 10000 → 1, 10001 → 2, 1234567 → 124. Ceiling division semantics.
- `TestRoundRow_PersistsScorerCost` — accept a round with fake scorer; assert `feature_debate_rounds.scorer_cost_micros` is set to the scorer's cost, not just rolled into `project_costs`.

**Stale-input race & in-flight reservation:**
- `TestCreateRound_RejectsStaleAIResponse` — start a round (call enters fake refiner that blocks on a channel); concurrently undo a prior accepted round (changes `current_text`); release the refiner; the in-flight INSERT detects `current_text != snapshotted_current_text` and returns 409, no round inserted.
- `TestCreateRound_RejectsParallelInFlight` — start a round (refiner blocks); attempt a second CreateRound on same debate; second returns 409 immediately because `in_flight_request_id` is set.
- `TestSeedEdit_BlockedDuringInFlight` — start a round (refiner blocks); attempt to edit seed; returns 409.
- `TestSeedEdit_BlockedAfterFirstRound` — accept round 1; attempt to edit seed; returns 400 ("seed frozen after round 1").
- `TestApprove_CASUsesOriginalNotSeed` — start debate, edit seed (still legal at round 0), accept a round, then approve; CAS uses `original_ticket_description` (still equal to ticket) so approve succeeds. Without this distinction (CAS against `seed_description`), the approve would always fail after a seed edit.

**Daily safety fuse:**
- `TestClientDailyCap_EnforcedAt50` — client triggers 50 rounds in 24h across multiple debates; 51st returns 429.
- `TestClientDailyCap_StaffBypass` — same scenario as staff → 51st succeeds.
- `TestClientDailyCap_RollsOver` — round inserted 25h ago doesn't count toward the cap.

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

### 10.1 Rolling-deploy compatibility (v0.1.0 → v0.2.0)

During a rolling deploy the cluster runs both v0.1.0 and v0.2.0 pods simultaneously for ~30 seconds. v0.1.0 pods do not know about debates and therefore do not enforce the `IsDebateActive` guard on `UpdateTicket`. A user who hits a v0.1.0 pod during this window can edit the description of a feature ticket that has an active debate.

The **CAS guard in `ApproveDebate` (§4.5 step 3)** catches this case: if the ticket's description no longer equals `feature_debates.seed_description` at approve time, the approve fails with 409 and the user is informed of the external edit. No silent overwrite is possible. This makes the feature safe to roll out without a multi-PR deployment choreography (a "guard-only" PR followed later by the main feature PR), at the cost of one rare-but-recoverable failure mode for users unlucky enough to be the test case.

## 11. Observability

- Per-round spend visible in `project_costs` under `category = 'debate'`.
- `feature_debates.status` gives active/approved/abandoned counts for reporting.
- Failed Refiner / Scorer calls logged at WARN with provider + model + truncated error; not surfaced to the user as errors (handler returns 502 with generic text for the round endpoint; silent for scorer).

---

*Design approved interactively 2026-04-14. Next step: writing-plans skill generates the detailed implementation plan, then file 10 v1 issues + 5 phase-2 issues on GitHub.*
