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
