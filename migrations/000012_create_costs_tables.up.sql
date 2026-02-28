-- AI model pricing (superadmin-managed lookup table)
CREATE TABLE ai_model_pricing (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    model_name TEXT NOT NULL UNIQUE,
    input_price_per_million_cents BIGINT NOT NULL,
    output_price_per_million_cents BIGINT NOT NULL,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed current Gemini pricing
INSERT INTO ai_model_pricing (model_name, input_price_per_million_cents, output_price_per_million_cents)
VALUES
    ('gemini-2.5-flash', 15, 60),
    ('gemini-2.5-pro', 125, 1000);

-- Monthly infrastructure cost line items per project
CREATE TABLE project_costs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    month TEXT NOT NULL,  -- 'YYYY-MM'
    category TEXT NOT NULL CHECK (category IN ('base_fee', 'dev_environment', 'testing_db', 'testing_container')),
    name TEXT NOT NULL DEFAULT '',
    amount_cents BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, month, category, name)
);
CREATE INDEX idx_project_costs_project_month ON project_costs(project_id, month);

-- Per-message AI usage log (automatic)
CREATE TABLE ai_usage_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id UUID REFERENCES projects(id) ON DELETE SET NULL,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    input_tokens INT NOT NULL DEFAULT 0,
    output_tokens INT NOT NULL DEFAULT 0,
    cost_cents BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ai_usage_org_created ON ai_usage_entries(org_id, created_at);
CREATE INDEX idx_ai_usage_project ON ai_usage_entries(project_id);
