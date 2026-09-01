-- Lets a user correct the AI's proposed text before accepting it (#66).
--
-- WHY A SEPARATE COLUMN RATHER THAN OVERWRITING output_text:
-- output_text keeps its meaning — what the AI produced. The cost columns and
-- the effort scorer bill for that text, so overwriting it would destroy the
-- only record of what the money bought. edited_text holds the user's version
-- when they changed something, NULL otherwise, and what ships is
-- COALESCE(edited_text, output_text).
--
-- No accompanying `edited_by_user` boolean, deliberately: a boolean beside the
-- text is a second source of truth that can drift from it, which is exactly
-- what #129 was in this same table. The column's presence IS the flag.
--
-- The CHECK stops an empty edit from silently emptying the brief; the handler
-- rejects it first, and this is the backstop.
-- btrim(x) with ONE argument strips only SPACES, so a tab- or newline-only
-- value survives it and reads as non-empty. Go's strings.TrimSpace rejects
-- those, so the explicit character set is what keeps this backstop as strict
-- as the handler it backs up (#66).
ALTER TABLE feature_debate_rounds
  ADD COLUMN edited_text TEXT
  CHECK (edited_text IS NULL OR btrim(edited_text, E' \t\n\r\f\v') <> '');
