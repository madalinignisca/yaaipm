# Per-Org Monthly Budget — Implementation Plan (issue #64)

> **For agentic workers:** implement task-by-task, TDD (RED before GREEN). Steps use
> checkbox syntax. Spec: `docs/superpowers/specs/2026-08-20-org-monthly-budget-design.md`
> (already survived an adversarial review round with Codex + Mistral Vibe).

**Branch:** `feature/64-org-monthly-budget`

**Step 0 is DONE.** The plan originally opened with "add a membership gate to `OrgSettings`",
which turned out to be a live cross-tenant leak. It shipped separately as PR #122 / v0.5.1.
The budget card can therefore be added to a page that is now correctly gated.

**Test env:**
```bash
docker compose -f docker-compose.test.yml up -d postgres && docker compose ... up migrate
go test ./internal/... -p 1 -count=1 -timeout 120s     # -p 1 REQUIRED (shared test DB)
```

## 1. Approach

One nullable `BIGINT` on `organizations`, a small audit table, one aggregate query over
`feature_debate_rounds`, and a guard in `CreateRound` after the existing fuses. No new
services, no cache, no advisory locks — the spec settled that this is a threshold, not a ledger.

**Staff are NOT exempt from the cap.** Spec §5 blocks `CreateRound` with no role qualifier and
§8 gives staff their own "raise it in organization settings" wording, which only makes sense if
staff hit the block. So the budget check sits OUTSIDE the `if !auth.IsStaffOrAbove(...)` block
that wraps the existing fuses. This differs from every other cap in that handler — deliberately.

## 2. Files

**New:** `migrations/000035_org_monthly_budget.{up,down}.sql`;
`internal/models/queries_budget_test.go`; `internal/handlers/orgs_budget_test.go`;
`internal/handlers/debate_budget_test.go`.

**Modified:** `internal/models/models.go`, `internal/models/queries.go`,
`internal/handlers/orgs.go`, `internal/handlers/debate.go`, `cmd/server/main.go`,
`templates/pages/org_settings.html`, `e2e/tests/12-debate-fake.spec.js`.

## 3. Steps

### Step 1 — migration + model column

```sql
-- Phase-2 issue #64: per-organization monthly AI debate budget.
-- Design: docs/superpowers/specs/2026-08-20-org-monthly-budget-design.md
--
-- monthly_budget_cents is USD cents, NOT the org's currency_code (spec §2):
-- provider pricing is quoted in USD and there is no FX anywhere in this
-- codebase, so rendering a USD-derived figure behind a EUR symbol would be a
-- false monetary representation. The UI labels it USD explicitly.
--
-- NULL = unlimited (the default for every existing org, so this ships
-- behaviourally inert). 0 is MEANINGFUL: it blocks new debate rounds while
-- still allowing the post-accept scorer and the retry sweep to finish work
-- already started (spec §5).
--
-- The CHECK matters because enforcement is `spend >= cap -> block`, and a
-- non-negative spend is always >= a negative cap: a negative value would
-- permanently block EVERY round for the org, a self-inflicted denial of
-- service that no UI would explain. (An earlier draft of this plan claimed
-- the opposite -- that a negative cap would unblock everything. It would
-- not. Corrected in Debate 2.)
--
-- Only that direction is pinned in schema; the USD 1M upper bound lives in
-- the parser AND the model setter, so a caller bypassing the handler still
-- cannot persist a value that overflows `cap * microsPerCent`.
ALTER TABLE organizations
    ADD COLUMN monthly_budget_cents BIGINT
        CHECK (monthly_budget_cents IS NULL OR monthly_budget_cents >= 0);

-- Cap changes are audited (spec §8). No existing table fits: ticket_activities
-- is ticket_id NOT NULL with a closed action CHECK. This is a money control,
-- so "who raised it, from what, to what, when" must survive container
-- restarts and log rotation.
--
-- changed_by is ON DELETE SET NULL, not CASCADE: deleting a user must not
-- erase the financial trail, and the row stays meaningful without them.
CREATE TABLE org_budget_changes (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    changed_by UUID REFERENCES users(id) ON DELETE SET NULL,
    old_cents  BIGINT CHECK (old_cents IS NULL OR old_cents >= 0),
    new_cents  BIGINT CHECK (new_cents IS NULL OR new_cents >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_org_budget_changes_org_created
    ON org_budget_changes (org_id, created_at DESC);
```

Down migration carries a LOSSY warning header (dropping the column resets every org to
unlimited and discards the audit trail; roll the application back instead).

- [ ] `Organization.MonthlyBudgetCents *int64` in `models.go` (fieldalignment is disabled in
      `.golangci.yml`, so placement is free — put it next to `AIMarginPercent`).
- [ ] Append `monthly_budget_cents` to `orgColumns` (`queries.go:370`) AND `&o.MonthlyBudgetCents`
      to `scanOrg` in the matching position. All nine org queries funnel through this pair, so
      it is a two-line change — but a mismatch is a RUNTIME error, not a compile error.

### Step 2 — `UpdateOrgMonthlyBudget` + audit, atomically

```go
var ErrOrgNotFound = errors.New("organization not found")

// Sets (non-nil) or clears (nil) the cap, recording old->new in
// org_budget_changes in the SAME transaction. Atomic on purpose: an
// unaudited change to a money control is worse than a failed change.
func (db *DB) UpdateOrgMonthlyBudget(ctx context.Context, orgID, actorUserID string, capCents *int64) error
```
Begin → `SELECT monthly_budget_cents ... FOR UPDATE` (ErrNoRows → `ErrOrgNotFound`) → UPDATE →
INSERT audit → Commit.

**Validate the range HERE too, not only in the parser** (Debate 2, both reviewers): reject
`*capCents > maxBudgetCents` (100_000_000) with `ErrBudgetOutOfRange`. The handler already
rejects it, but a future or internal caller bypassing the handler could otherwise persist a
value that overflows `cap * microsPerCent` at comparison time. The DB CHECK guards the negative
direction; this guards the positive one.

### Step 3 — spend aggregate + month range

```go
// Totals raw provider spend for one org's debate rounds in [from, to).
// Refiner AND scorer cost, all round statuses — a rejected suggestion
// still cost money.
//
// Enforcement source of truth (spec §3), NOT project_costs:
// IncrementProjectCostCents runs after commit and is explicitly non-fatal,
// so one failed rollup would permanently undercount and silently lift the
// cap. These two columns are written inside the round transaction.
//
// Returns 0,nil when there are no rounds; callers MUST treat a non-nil
// error as "unknown", never as zero.
func (db *DB) SumOrgDebateSpendMicros(ctx context.Context, orgID string, from, to time.Time) (int64, error)
```
```sql
SELECT COALESCE(SUM(COALESCE(r.cost_micros,0) + COALESCE(r.scorer_cost_micros,0)), 0)::BIGINT
  FROM feature_debate_rounds r
  JOIN feature_debates d ON d.id = r.debate_id
 WHERE d.org_id = $1 AND r.created_at >= $2 AND r.created_at < $3
```
The inner COALESCEs matter: both columns are nullable, and `NULL + 5` is `NULL`, which would
drop a whole round from the sum whenever the scorer had not run.

The trailing `::BIGINT` is for type clarity, not a bug fix: `SUM(bigint)` returns `numeric` in
PostgreSQL, and it was VERIFIED empirically that pgx scans that into `int64` without error. The
cast documents the intended width and turns a hypothetical overflow into a clean DB error rather
than a driver conversion surprise. (Debate 2 claimed the uncast query would not work — it does.)

**Assumption, documented rather than enforced:** `cost_micros`/`scorer_cost_micros` are never
negative — they come from `ComputeCostMicros`, which cannot return a negative. If that ever
changes, a negative sum would let spend fall below a cap of 0 and unblock rounds.

Half-open range, not `to_char(...) = '2026-08'` — a to_char predicate is not sargable and would
scan every round. Bucket identity is preserved by DERIVING the range from the same string:

```go
// Returns [start,end) of the UTC month containing now — the same bucket
// IncrementProjectCostCents writes via Format("2006-01") (spec §7).
//
// No error return: constructing the boundaries directly from now with
// time.Date(...) in UTC cannot fail, whereas the earlier Format-then-Parse
// round-trip introduced an impossible error path callers had to handle.
// MUST use the passed `now`, never time.Now() internally, or the caller's
// captured instant and the bucket can straddle midnight.
func currentUTCMonthRange(now time.Time) (start, end time.Time)
```
No new index: the planner uses `idx_feature_debates_org_status` + `idx_feature_debate_rounds_debate`.
Measure with EXPLAIN ANALYZE before adding one.

### Step 4 — money parsing

Do NOT reuse `parseToCents` (`costs.go:320`) — it uses ParseFloat + math.Round, which the spec
forbids and which silently accepts `1e3`/`NaN`/`Inf` and rounds `25.555`. Changing it would alter
existing cost-entry behaviour.

```go
// Decimal USD string -> integer cents, no floating point. Accepts an
// optional leading '$' and surrounding whitespace; otherwise must match
// ^\d+(\.\d{1,2})?$
func parseUSDCents(s string) (int64, error)
func formatUSDCents(cents int64) string   // fmt.Sprintf("%d.%02d", c/100, c%100)
//   MUST guard negatives: %02d of a negative remainder renders "0.-5", not
//   "-0.05". Callers should never pass one, so treat it as a bug-catcher:
//   clamp to "0.00" or format the absolute value with a leading '-'.
```
Distinct sentinels so the handler returns distinct 400s: `errBudgetNotANumber`,
`errBudgetTooManyDecimals` (3+ decimals REJECTED, never rounded), `errBudgetOutOfRange`
(> 100_000_000 cents; check the dollar part against 1_000_000 BEFORE multiplying so the guard
cannot itself overflow). Implementation: `strings.Cut(s,".")`, `ParseInt` each half, right-pad the fraction to 2 digits,
`dollars*100 + frac`.

**Validate the lexical form BEFORE ParseInt** — `ParseInt` accepts a leading `+` and would let
`+25` through a grammar that declares it invalid. Check every byte is an ASCII digit (after
trimming the optional `$` and whitespace, and splitting on at most one `.`) rather than relying
on ParseInt to reject non-conforming input.

### Step 5 — set handler + route

```go
// POST /orgs/{orgSlug}/settings/budget
// NOT staff-only, unlike UpdateAIMargin: the budget is the client's own
// spend control (spec §6).
func (h *OrgHandler) UpdateMonthlyBudget(w http.ResponseWriter, r *http.Request)
```
1. `GetOrgBySlug` → 404 on ErrNoRows, 500 otherwise (keep DISTINCT; `UpdateAIMargin` currently
   collapses them — do not copy that).
2. `h.canManageOrgMembers(r, user, org.ID)` → 403. That helper is already exactly spec §6.
3. Empty field → `capCents = nil` (unlimited). Else `parseUSDCents` with one message per sentinel.
4. `UpdateOrgMonthlyBudget` → distinct `ErrOrgNotFound` (404) / generic (500) branches.
5. `log.Printf` alongside the DB audit row; redirect 303 to settings.

**The org comes from the route slug only** — no `org_id` form field is read. Cross-org
substitution is covered by a test.

Route in `cmd/server/main.go` inside the auth+CSRF group.

### Step 6 — org settings card

`OrgSettings` adds Go-computed strings (no float in templates, no new FuncMap entries):
`CanViewBudget`, `BudgetIsUnlimited`, `BudgetCapInput`, `BudgetCapDisplay`,
`BudgetSpendDisplay` (micros→cents half-up: `(micros+5000)/10000`), `BudgetSpendUnavailable`,
`BudgetMonth`.

**Capture `now` ONCE** and derive both the aggregate range and `BudgetMonth` from it, so the
displayed month can never disagree with the figure beside it across a midnight boundary.

An aggregate failure here is NOT fatal to the page — show a dash. Deliberately different from
the enforcement path: showing "unavailable" is harmless, treating unknown spend as zero at the
gate is not.

Template: insert BEFORE the `{{if .IsStaff}}` block. Gating is `{{if .CanViewBudget}}` (members
see it — the margin card is staff-only) with the form itself behind `{{if .CanManage}}`. Reuse
the margin card's DaisyUI classes verbatim so no new utility classes are introduced. Always
`$`/USD, never `{{.Org.CurrencyCode}}`. Helper text: *"Applies to AI feature-refinement spend
only. Leave blank for unlimited. Set to 0 to block new AI suggestions — work already in
progress still finishes."*

### Step 7 — enforcement in `CreateRound` (the core)

```go
var (
    errBudgetExceeded    = errors.New("org monthly budget reached")
    errBudgetUnavailable = errors.New("org monthly spend could not be determined")
)

// nil = uncapped or under cap; errBudgetExceeded at/over cap;
// errBudgetUnavailable when the aggregate could not be read — callers MUST
// fail closed (spec §3): treating "unknown" as zero turns a transient DB
// error into unlimited spend.
func (h *DebateHandler) checkOrgBudget(ctx context.Context, org *models.Organization) error
```
Compare in MICROS, no rounding: `spendMicros >= *org.MonthlyBudgetCents * microsPerCent`
(`const microsPerCent = 10_000`). Max cap 1e8 cents × 1e4 = 1e12 — nowhere near int64 overflow.

Insertion point: AFTER the closing brace of the `if !auth.IsStaffOrAbove(...)` fuse block
(~line 409) and BEFORE the reservation tx. Guarantees no AI call and no debate-row mutation.

Three distinct branches: nil → continue; `errBudgetExceeded` → 429 + role-appropriate message;
`errBudgetUnavailable` → 503 + generic infra copy (NOT 429 — "we couldn't verify your budget" is
not a rate limit and must not tell a client to wait until next month).

`dctx.org` comes from AuthMiddleware, which reloads orgs from the DB on every request — so there
is no session-cached org. It is NOT, however, a staleness guarantee: an admin can lower the cap
between that load and this guard, so the request proceeds against the older value. That is one
more instance of the bounded overshoot the spec already accepts (§4), not a separate defect —
but the plan should not claim the reload prevents it.

Message selection does ONE `GetOrgMembership` lookup inside the message function only, so the
hot path pays nothing until the cap actually trips. A lookup error falls back to the CLIENT
wording (least informative is the safe default).

### Step 8 — E2E + docs

ONE test added to `e2e/tests/12-debate-fake.spec.js` (CI runs only that spec against a fresh DB;
do not add a new self-registering spec): set the budget to 0, request a suggestion, assert the
flash appears and no round card is added. Zero is the only workable value — fake rounds cost a
fraction of a cent and would never trip a realistic nonzero cap.

Verify which role the self-registering E2E user ends up with (first user is auto-promoted to
superadmin) and assert the matching wording.

## 4. Test plan

**Models:** set/clear round-trip; audit row correctness (first change has `old_cents IS NULL`);
raw `-1` INSERT must error (proves the CHECK); unknown org → `ErrOrgNotFound` with NO audit row;
`SumOrgDebateSpendMicros` — empty → 0,nil; sums both columns incl. NULL scorer; excludes other
orgs; excludes out-of-range rows testing BOTH edges; counts all round statuses.

**Handler (set):** `parseUSDCents` table test (accept `0`,`25`,`25.5`,`25.50`,`$25.00`,` 25.00 `,
`1000000`; reject ``,`-1`,`25.555`,`1e3`,`abc`,`1,000`,`NaN`,`Inf`,`1000000.01`,`25.`,`.50`);
role matrix through the real router (member 403, owner 303, admin 303, staff-on-foreign-org 303,
non-member 403); cross-org substitution; empty clears to NULL; card renders for a member, form
hidden.

**Handler (enforcement):** cap NULL → created; under cap → created; at/over cap → 429 **and
`FakeRefiner.CallCount == 0`** and no new round row; cap 0 → CreateRound 429 but StartDebate/
accept/undo/approve still succeed; role-appropriate wording (note `seedAuthedFeatureTicket`
makes the user the OWNER, so the client-wording case needs a direct role downgrade);
rollover (previous-month rounds don't count); scorer cost counts toward the cap.

Added in Debate 2:
- **cap 0 with spend 0 must BLOCK** (`0 >= 0`). The obvious-looking boundary, and the one an
  off-by-one in the comparison would flip.
- **Fail-closed needs a real seam, not a cancelled context.** A cancelled ctx fails auth or the
  ticket lookup first, so the test would pass without ever reaching the aggregate. Isolate the
  aggregate failure specifically — e.g. call `checkOrgBudget` directly (it is package-private
  and the test is in `package handlers`) with a stub/broken pool — and assert 503, zero refiner
  calls, and no round row.
- **Audit rows:** assert `changed_by` is the actor, exactly one row per successful update, and
  **no row at all** when the update fails (proving the transaction is atomic).
- **Cross-org set:** a non-member of org A attempting to set org B's budget → 403.
- **Overflow:** a cap at the 100M bound with large seeded costs must still compare correctly and
  block, not wrap or 503.

**E2E:** assert the settings POST actually persisted `0` (redirect plus the rendered zero) BEFORE
asserting the suggestion is blocked — otherwise an unrelated pre-existing block satisfies the
test. Confirm the acting user's role and assert the matching wording branch.

## 5. Risks

- **R2 — Undo erases spend from the aggregate.** `UndoRoundsFromTx` hard-DELETEs rounds, so a
  user near the cap can undo to reclaim headroom for money already spent. Not a reason to change
  §3; file as a follow-up.
- **R3 — Un-priced models record `cost_micros = 0`** (the #108 failure mode), so an org on an
  un-priced model never trips its cap. The `HasPricing` startup warning already exists; this
  makes it a money-correctness signal, not just reporting.
- **R4 — Costs page and budget card will show different numbers** (per-project vs org-wide, with
  margin vs without). Both correct; the helper text is the mitigation.
- **R5 — Migration is additive**; `ADD COLUMN ... NULL` with no default is catalog-only on PG 11+.
  The DOWN is destructive — roll the app back instead.
- **R6 — GDPR:** `org_budget_changes.changed_by` links a person to an action. `ON DELETE SET NULL`
  keeps the financial record while removing the identifier on erasure. No new data leaves the EU.
- **R7 — Overshoot is expected**; no test should assert a hard ceiling.
- **R8 — Rounding:** comparison in micros with no rounding; only DISPLAY cents are rounded.

## 6. Out of scope

Spec §10, plus: fixing `parseToCents`'s float arithmetic; the undo hole (R2); surfacing the
budget on the costs page or debate sidebar; any UI for `org_budget_changes`; changes to
`ClientDailyRoundCap`.
