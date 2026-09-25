-- No cascading run/request FK: deleting display history must not release
-- capacity while upstream execution may still be live.
CREATE TABLE review_executions (
 attempt_id uuid PRIMARY KEY,
 org_id uuid NOT NULL,
 request_id uuid NOT NULL,
 lease_id uuid NOT NULL,
 terminated_at timestamptz,
 termination_evidence text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON review_executions(org_id,request_id) WHERE terminated_at IS NULL;
-- Old workers did not retain termination receipts. Never infer termination
-- from their expired leases or failed transport calls during an upgrade.
INSERT INTO review_executions(attempt_id,org_id,request_id,lease_id,termination_evidence)
 SELECT id,org_id,id,COALESCE(lease_id,'00000000-0000-0000-0000-000000000000'),'legacy_unreconciled'
 FROM agent_review_requests WHERE state IN ('running','uncertain') OR (state='failed' AND attempts<>'[]'::jsonb);
