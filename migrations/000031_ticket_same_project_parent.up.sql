-- Fix #26: enforce same-project parentage and cascade deletes recursively.
--
-- The old schema let parent_id reference any ticket in any project, and
-- ON DELETE SET NULL left descendants behind as orphans. We replace the
-- single-column FK with a composite FK on (project_id, parent_id) so the
-- database itself rejects cross-project parents, and switch to CASCADE
-- so DELETE recurses automatically.

-- First, repair any existing cross-project links by nulling them out.
-- Leaving them in place would make the new FK creation fail.
UPDATE tickets c
SET parent_id = NULL
FROM tickets p
WHERE c.parent_id = p.id
  AND c.project_id <> p.project_id;

-- A composite FK needs a matching UNIQUE/PRIMARY KEY on the target.
-- PK is on (id) alone; add an explicit UNIQUE on (project_id, id).
ALTER TABLE tickets
    ADD CONSTRAINT tickets_project_id_id_key UNIQUE (project_id, id);

-- Drop the old single-column FK (pg names it tickets_parent_id_fkey).
ALTER TABLE tickets
    DROP CONSTRAINT tickets_parent_id_fkey;

-- Add the composite FK with CASCADE so deletes walk the whole subtree.
ALTER TABLE tickets
    ADD CONSTRAINT tickets_parent_fkey
    FOREIGN KEY (project_id, parent_id)
    REFERENCES tickets (project_id, id)
    ON DELETE CASCADE;
