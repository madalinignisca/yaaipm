-- Feature Debate Mode tables. See design spec at
-- docs/superpowers/specs/2026-04-14-feature-debate-mode-design.md §3.1.
--
-- Migration ordering handles the cyclic FK between feature_debates and
-- feature_debate_rounds: create feature_debates first (without the FK),
-- create feature_debate_rounds, then ALTER TABLE to add last_scored_round_id.

CREATE TABLE feature_debates (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id                   UUID NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    project_id                  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id                      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    started_by                  UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status                      TEXT NOT NULL DEFAULT 'active'
                                CHECK (status IN ('active','approved','abandoned')),
    seed_description            TEXT NOT NULL,
    current_text                TEXT NOT NULL,
    original_ticket_description TEXT NOT NULL,
    in_flight_request_id        UUID,
    in_flight_started_at        TIMESTAMPTZ,
    total_cost_micros           BIGINT NOT NULL DEFAULT 0,
    effort_score                INT,
    effort_hours                INT,
    effort_reasoning            TEXT,
    effort_scored_at            TIMESTAMPTZ,
    approved_text               TEXT,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Feature-only invariant placeholder (spec §3.1). Enforced in Go for v1
    -- so ticket.type changes do not retroactively invalidate old debates.
    -- This CHECK(true) is a schema marker for future relaxation: a later
    -- migration can replace it with a real CHECK if/when we widen scope.
    CONSTRAINT feature_debates_ticket_type_stub CHECK (true)
);

CREATE TABLE feature_debate_rounds (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    debate_id           UUID NOT NULL REFERENCES feature_debates(id) ON DELETE CASCADE,
    round_number        INT NOT NULL,
    provider            TEXT NOT NULL CHECK (provider IN ('claude','gemini','openai')),
    model               TEXT NOT NULL,
    triggered_by        UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    feedback            TEXT,
    input_text          TEXT NOT NULL,
    output_text         TEXT NOT NULL,
    diff_unified        TEXT,
    status              TEXT NOT NULL DEFAULT 'in_review'
                        CHECK (status IN ('in_review','accepted','rejected')),
    input_tokens        INT,
    output_tokens       INT,
    cost_micros         BIGINT,
    scorer_cost_micros  BIGINT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at          TIMESTAMPTZ,
    UNIQUE (debate_id, round_number)
);

-- Break the cyclic FK by adding last_scored_round_id after both tables exist.
-- ON DELETE SET NULL is critical — without it, Undo deleting the referenced
-- round would fail with a FK violation.
ALTER TABLE feature_debates
    ADD COLUMN last_scored_round_id UUID
        REFERENCES feature_debate_rounds(id) ON DELETE SET NULL;

-- Indexes on feature_debates
CREATE UNIQUE INDEX idx_feature_debates_one_active_per_ticket
    ON feature_debates (ticket_id) WHERE status = 'active';
CREATE INDEX idx_feature_debates_ticket       ON feature_debates (ticket_id);
CREATE INDEX idx_feature_debates_org_status   ON feature_debates (org_id, status);
CREATE INDEX idx_feature_debates_project      ON feature_debates (project_id);
CREATE INDEX idx_feature_debates_started_by   ON feature_debates (started_by);

-- Indexes on feature_debate_rounds
CREATE INDEX idx_feature_debate_rounds_debate
    ON feature_debate_rounds (debate_id, round_number DESC);
CREATE INDEX idx_feature_debate_rounds_debate_status_number
    ON feature_debate_rounds (debate_id, status, round_number DESC);
CREATE INDEX idx_feature_debate_rounds_triggered_by_created
    ON feature_debate_rounds (triggered_by, created_at DESC);

-- One in-review round per debate (enforced at DB layer, so handler can
-- skip application-level locking across the AI call — see spec §4.2).
CREATE UNIQUE INDEX idx_feature_debate_rounds_one_in_review_per_debate
    ON feature_debate_rounds (debate_id) WHERE status = 'in_review';

-- Expand project_costs category CHECK to allow 'debate' rollup category.
-- One-way: the down migration does NOT restore the narrower constraint;
-- see spec §3.1.ter for the financial-audit-trail rationale.
ALTER TABLE project_costs DROP CONSTRAINT IF EXISTS project_costs_category_check;
ALTER TABLE project_costs ADD CONSTRAINT project_costs_category_check
    CHECK (category IN ('base_fee', 'dev_environment', 'testing_db',
                        'testing_container', 'debate'));
