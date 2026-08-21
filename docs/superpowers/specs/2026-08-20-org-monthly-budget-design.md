# Per-Org Monthly Budget with Enforcement — Design Spec (issue #64)

**Status:** agreed after adversarial review by Codex (`codex-cli 0.145.0`) and Mistral Vibe
(`vibe 2.24.1`). Their must-fix findings are folded in below; §9 records what was rejected
and why. Phase-2 issue B of `docs/superpowers/specs/2026-04-14-feature-debate-mode-design.md` §8.

## 1. Goal

Give each organization a monthly cap on AI **debate** spend. Once the month's debate spend
reaches the cap, new debate rounds are refused for that org until the next UTC month. The
existing per-user daily round fuse (`ClientDailyRoundCap`, clients only) **stays** as defense
in depth — this supplements it, it does not replace it.

## 2. Money: units, currency, and which number is capped

- **The cap is denominated in USD**, stored as a nullable non-negative `BIGINT` of **USD
  cents**. Every UI label says USD explicitly.
- **`organizations.currency_code` is NOT used for the budget.** It is display-only elsewhere
  (the margin card states "Does not convert amounts"), and rendering a USD-derived figure
  behind a `€` symbol would be a false monetary representation. No FX, no conversion — the
  budget card is labelled USD regardless of the org's display currency.
- **The capped figure is raw provider spend, with no `ai_margin_percent` applied.** Verified:
  margin is applied at display time only, and only to `ai_usage_entries` (`costs.go:97-99`);
  debate spend reaches clients un-marked-up inside `infraTotal`. Capping a marked-up number
  would enforce a figure displayed nowhere.

## 3. Enforcement source of truth — `feature_debate_rounds`, not `project_costs`

`IncrementProjectCostCents` writes `project_costs` **after** the round completes and is
**explicitly non-fatal** (failures are logged, not returned). Using it to enforce a budget
would mean a single failed rollup permanently undercounts spend and silently lifts the cap.

Therefore the budget check aggregates the **durable, in-transaction** per-round record:
`feature_debate_rounds.cost_micros + scorer_cost_micros`, joined to the org via
`feature_debates.org_id`, filtered to the current UTC month. `project_costs` remains the
display/rollup path and is unchanged.

**Fail closed.** If the spend aggregate cannot be read, refuse the round. This costs nothing
in practice: the round insert needs the same database, so a DB outage fails the request
regardless — but it removes "aggregate errored → treated as zero → unlimited spend".

## 4. Enforcement semantics — a best-effort threshold, not a hard ceiling

Cost is recorded after the provider call, so N concurrent rounds can each pass the check
before any of their costs land. This is a **threshold that stops new work once observed spend
reaches the cap**, not a guarantee that spend never exceeds it. Stated plainly so nobody
implements it believing otherwise.

**Bounded overshoot.** `reserveInFlight` allows at most one in-flight round per debate, so
worst case is `(concurrently active debates in the org) × (max cost of one refiner + scorer
round)`. No advisory lock, no cost pre-reservation: the complexity is not justified for a
control whose purpose is stopping sustained spend, not cent-exact billing.

## 5. What is and is not blocked

| Action | Blocked at cap? | Why |
|---|---|---|
| `CreateRound` (new AI round) | **Yes** — 429 | The only place new spend is initiated |
| `StartDebate` | No | No AI call, costs nothing |
| Accept / reject / undo / approve | No | No AI call |
| Post-accept scorer | No | Attaches to an already-created round |
| Background retry sweep | No | Same; blocking strands debates permanently unscored |

Scorer and sweep spend still **counts** toward the month, so it shrinks headroom for the next
round — the correct pressure point. Blocking them would leave accepted debates with
`effort_*` NULL forever to save cents.

Because of this, an explicit cap of `0` means **"block new rounds"**, not "stop all AI spend".
The UI must use that wording.

## 6. Configuration, roles, and validation

- **Who may set it:** org owner/admin (`auth.CanManageOrg`) **and** staff/superadmin. This
  deviates from `ai_margin_percent` (staff-only) deliberately: margin is the consultancy's
  billing knob *about* a client; the budget is the client's own spend control, and the issue
  says "owners set". Staff/superadmin may set it for **any** org, consistent with the margin
  precedent.
- **Who may view it:** any org member sees their own org's cap and current spend (the costs
  page is already member-visible and already shows debate spend). Staff see all orgs. No new
  per-provider or per-model granularity is exposed anywhere — aggregate figures only.
- **The org is always derived from the trusted route/session context**, never from a
  form field. Cross-org substitution must be covered by a test.
- **Validation:** cap is nullable (`NULL` = unlimited). When set it must be `>= 0` — enforced
  by handler validation, a DB `CHECK`, **and** the model-layer setter, so a caller that
  bypasses the handler still cannot persist an invalid value.

  *Correction (Debate 2):* an earlier draft of this spec claimed a negative cap would
  "silently unblock everything". That is backwards. Enforcement is
  `spend >= cap → block`, and a non-negative spend is always `>= a negative cap`, so a
  negative cap would **permanently block every round** — a self-inflicted denial of
  service, not a bypass. The constraint is still required; the reason is the opposite of
  what was written.

  Upper bound `100_000_000` cents (USD 1M) — enforced in the setter as well as the parser,
  because `cap * microsPerCent` overflows int64 for an unbounded BIGINT;
  input parsed from a decimal string to integer cents **without floating point**; more than
  two decimal places is rejected rather than rounded.

## 7. Month boundary

The check uses `time.Now().UTC().Format("2006-01")`, byte-identical to the writer in
`IncrementProjectCostCents`. Spend is attributed by **cost-write time** (existing behavior);
the check reads **creation time**. A round created seconds before rollover whose cost lands
after it is charged to the new month — a one-round wobble, accepted and documented rather
than engineered away. At rollover the new bucket sums to zero and blocked orgs unblock with
no intervention.

## 8. Responses and messaging

- HTTP **429**, matching the existing cap responses.
- **Client wording avoids AI internals**: "Your organization's monthly budget has been
  reached. It resets at the start of next month." No provider names, no model IDs, no
  per-round figures.
- **Owner/admin/staff wording is actionable and names the setting**, since they can change
  it: "Monthly AI budget reached for this organization — raise it in organization settings
  to continue."
- The budget check runs **after** the existing in-flight and daily-fuse guards, so the more
  specific error wins and no response leaks provider details.
- **Cap changes are audited** (actor, org, old → new, timestamp). It is a money control; a
  silent change is indistinguishable from a bug.

## 9. Review findings rejected, with reasons

- **"Showing spend to owners/clients violates the no-AI-internals rule."** Rejected: verified
  that `costs.go` gates on org *membership*, not role, and debate spend is already inside the
  `infraTotal` clients see. Aggregate cost is the costs feature's entire purpose; the rule
  covers provider/model internals. The narrower instinct is honored — no new granularity.
- **"Introduce a new authoritative per-round cost record."** Rejected as redundant:
  `feature_debate_rounds.cost_micros` / `scorer_cost_micros` are already written in the round
  transaction and already durable. §3 adopts the intent by aggregating from them.

## 10. Out of scope

Assistant/chat spend (`ai_usage_entries`); per-project budgets; carryover; proration; FX;
threshold notifications (email/webhook/Slack); forecasting; changes to the daily fuse or the
retry sweep; distributed locking or transactional cost reservation.

## 11. Success criteria

1. Org with no cap: behaviorally identical to today; every existing debate test passes.
2. Cap set, spend below: `CreateRound` succeeds.
3. Cap set, current-month debate spend >= cap: 429, role-appropriate message, **no AI call**,
   no debate row mutated.
4. Cap `0`: new rounds blocked; StartDebate, accept/undo/approve, scorer and sweep still work.
5. Negative cap rejected by handler and by DB constraint.
6. Spend aggregate read failure ⇒ round refused (fail closed).
7. Rollover: a blocked org creates rounds in the next UTC month with no intervention.
8. Role matrix enforced, including staff-any-org and cross-org substitution.
9. Aggregation counts scorer cost, not just refiner cost.
