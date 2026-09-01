-- Dropping this loses the user's corrections on every edited round; the text
-- that actually shipped exists nowhere else, since output_text deliberately
-- holds the AI's original.
ALTER TABLE feature_debate_rounds DROP COLUMN IF EXISTS edited_text;
