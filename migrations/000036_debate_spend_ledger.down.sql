-- Dropping the ledger returns enforcement to summing live round rows, which is
-- the #129 bug: undo reclaims headroom for money already spent. Rolling back
-- loses every spend record for rounds that were undone while this was live —
-- that history exists nowhere else.
DROP TABLE IF EXISTS debate_spend;
