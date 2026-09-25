DROP INDEX agent_grant_cleanup_pending;
CREATE INDEX agent_grant_cleanup_pending ON agent_runs(grant_cleanup_next_at,ended_at,id) WHERE grant_cleanup_pending OR work_release_pending;
ALTER TABLE agent_runs DROP COLUMN workspace_identity, DROP COLUMN workspace_cleanup_pending, DROP COLUMN workspace_cleanup_error;
