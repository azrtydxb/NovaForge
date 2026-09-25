ALTER TABLE agent_runs
 ADD COLUMN grant_cleanup_pending boolean NOT NULL DEFAULT false,
 ADD COLUMN grant_cleanup_error text NOT NULL DEFAULT '';
-- Existing terminal runs may still hold live grants. Retry only exact ids;
-- revocation is idempotent and belongs to the grant owner's service.
UPDATE agent_runs SET grant_cleanup_pending = true
 WHERE state IN ('succeeded','failed','cancelled','over_budget')
 AND grant_id <> '00000000-0000-0000-0000-000000000000';
CREATE INDEX agent_grant_cleanup_pending ON agent_runs (ended_at,id)
 WHERE grant_cleanup_pending;
