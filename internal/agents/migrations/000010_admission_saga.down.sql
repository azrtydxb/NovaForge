DROP INDEX agent_grant_cleanup_pending;
CREATE INDEX agent_grant_cleanup_pending ON agent_runs(grant_cleanup_next_at,ended_at,id) WHERE grant_cleanup_pending;
ALTER TABLE agent_runs DROP COLUMN grant_issue_intent, DROP COLUMN admission_lease_until, DROP COLUMN work_claim_required, DROP COLUMN work_item_snapshot, DROP COLUMN work_release_pending, DROP COLUMN work_release_error, DROP COLUMN execution_finished;
