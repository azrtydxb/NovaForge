ALTER TABLE agent_runs ADD COLUMN grant_issue_intent jsonb,
 ADD COLUMN admission_lease_until timestamptz NOT NULL DEFAULT '-infinity',
 ADD COLUMN work_claim_required boolean NOT NULL DEFAULT false,
 ADD COLUMN work_item_snapshot bytea,
 ADD COLUMN work_release_pending boolean NOT NULL DEFAULT false,
 ADD COLUMN work_release_error text NOT NULL DEFAULT '',
 ADD COLUMN execution_finished boolean NOT NULL DEFAULT false;
DROP INDEX agent_grant_cleanup_pending;
CREATE INDEX agent_grant_cleanup_pending ON agent_runs(grant_cleanup_next_at,ended_at,id) WHERE grant_cleanup_pending OR work_release_pending;
