ALTER TABLE agent_runs ADD COLUMN workspace_identity jsonb,
 ADD COLUMN workspace_cleanup_pending boolean NOT NULL DEFAULT false,
 ADD COLUMN workspace_cleanup_error text NOT NULL DEFAULT '';
DROP INDEX agent_grant_cleanup_pending;
CREATE INDEX agent_grant_cleanup_pending ON agent_runs(grant_cleanup_next_at,ended_at,id) WHERE grant_cleanup_pending OR work_release_pending OR workspace_cleanup_pending;
