CREATE TABLE agent_review_requests (
 id uuid PRIMARY KEY,
 org_id uuid NOT NULL,
 run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 requested_by uuid NOT NULL,
 source_sha text NOT NULL,
 target_sha text NOT NULL,
 state text NOT NULL CHECK (state IN ('queued','running','succeeded','failed','uncertain')),
 detail text NOT NULL DEFAULT '',
 attempts jsonb NOT NULL DEFAULT '[]',
 lease_id uuid,
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (run_id, source_sha, target_sha)
);
CREATE INDEX ON agent_review_requests(org_id,state,created_at);
