DROP INDEX IF EXISTS repositories_by_parent;
ALTER TABLE repositories DROP COLUMN IF EXISTS parent_repo_id;
