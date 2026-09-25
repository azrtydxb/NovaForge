DROP INDEX agent_grant_cleanup_pending;
CREATE INDEX agent_grant_cleanup_pending ON agent_runs (ended_at,id) WHERE grant_cleanup_pending;
ALTER TABLE agent_runs DROP COLUMN grant_cleanup_next_at, DROP COLUMN grant_cleanup_lease_until, DROP COLUMN grant_cleanup_claim, DROP COLUMN grant_cleanup_attempts;
