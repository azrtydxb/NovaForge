ALTER TABLE agent_runs ADD COLUMN grant_cleanup_next_at timestamptz NOT NULL DEFAULT '-infinity',
    ADD COLUMN grant_cleanup_lease_until timestamptz NOT NULL DEFAULT '-infinity',
    ADD COLUMN grant_cleanup_claim uuid,
    ADD COLUMN grant_cleanup_attempts bigint NOT NULL DEFAULT 0;
DROP INDEX agent_grant_cleanup_pending;
CREATE INDEX agent_grant_cleanup_pending ON agent_runs (grant_cleanup_next_at, ended_at, id) WHERE grant_cleanup_pending;
