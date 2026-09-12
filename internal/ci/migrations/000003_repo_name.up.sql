-- A clone URL and the on-disk repository path are keyed by name, but the run
-- carries only the id, and CI must not read git-platform's schema to resolve
-- one from the other. The name travels on the push event and is stored here.
ALTER TABLE ci.workflow_runs ADD COLUMN IF NOT EXISTS repo_name text NOT NULL DEFAULT '';
