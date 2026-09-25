ALTER TABLE run_proof ADD COLUMN producer text NOT NULL DEFAULT '';
ALTER TABLE run_proof ADD COLUMN actor_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
CREATE TABLE proof_audit (
 id bigserial PRIMARY KEY,
 run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 gate text NOT NULL,
 status text NOT NULL,
 detail text NOT NULL,
 producer text NOT NULL,
 actor_id uuid NOT NULL,
 recorded_at timestamptz NOT NULL
);
INSERT INTO proof_audit(run_id,gate,status,detail,producer,actor_id,recorded_at)
 SELECT run_id,gate,status,detail,producer,actor_id,recorded_at FROM run_proof;
CREATE INDEX ON proof_audit(run_id,id);
