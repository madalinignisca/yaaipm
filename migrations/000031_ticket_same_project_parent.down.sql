-- Revert same-project parent enforcement.

ALTER TABLE tickets
    DROP CONSTRAINT tickets_parent_fkey;

ALTER TABLE tickets
    DROP CONSTRAINT tickets_project_id_id_key;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_parent_id_fkey
    FOREIGN KEY (parent_id)
    REFERENCES tickets (id)
    ON DELETE SET NULL;
