-- agent_runs did not originally carry a repo_id column: Tasks 1-2 scoped a
-- run to (org_id, branch) only. The branch lock needs one more dimension —
-- two different repositories may legitimately use the same branch name —
-- so this migration adds it, backfilled to the nil UUID for any existing
-- row, and then adds the partial unique index that is the actual lock: the
-- database, not application code, refuses a second run to move into
-- 'running' for a branch some other run already holds.
ALTER TABLE agent_runs
    ADD COLUMN IF NOT EXISTS repo_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';

CREATE UNIQUE INDEX IF NOT EXISTS agent_branch_lock
    ON agent_runs (org_id, repo_id, branch)
    WHERE state = 'running';
