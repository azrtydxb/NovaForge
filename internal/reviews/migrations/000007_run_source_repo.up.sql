-- An Engineering Run's source ref may live in another repository — a fork of
-- the target proposing a change back to it.
--
-- The column is nullable and every read applies COALESCE(source_repo_id,
-- repo_id), so a row written by code that predates forks (or by a caller that
-- does not set it) describes a run in its own repository, exactly as it always
-- did. A NOT NULL DEFAULT would have needed the default to be another column's
-- value, which SQL cannot express, and filling it in a trigger would put the
-- rule somewhere no reader of the queries would look.
ALTER TABLE runs ADD COLUMN source_repo_id uuid;

-- Existing rows are backfilled as well, so the column agrees with the COALESCE
-- rather than relying on it: a later query that forgets the COALESCE then still
-- reads the right repository for every run that existed before this migration.
UPDATE runs SET source_repo_id = repo_id WHERE source_repo_id IS NULL;
