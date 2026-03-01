CREATE TABLE ai_model_pricing (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    model_name TEXT NOT NULL UNIQUE,
    input_price_per_million_cents BIGINT NOT NULL,
    output_price_per_million_cents BIGINT NOT NULL,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO ai_model_pricing (model_name, input_price_per_million_cents, output_price_per_million_cents)
VALUES
    ('gemini-2.5-flash', 15, 60),
    ('gemini-2.5-pro', 125, 1000);
