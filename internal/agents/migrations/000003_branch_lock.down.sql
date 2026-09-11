DROP INDEX IF EXISTS agent_branch_lock;
ALTER TABLE agent_runs DROP COLUMN IF EXISTS repo_id;
