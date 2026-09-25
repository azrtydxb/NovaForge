DROP INDEX agent_grant_cleanup_pending;
ALTER TABLE agent_runs DROP COLUMN grant_cleanup_pending, DROP COLUMN grant_cleanup_error;
