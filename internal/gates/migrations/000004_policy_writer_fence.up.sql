-- A rolling-upgrade predecessor does not update policy_sha on conflict. Keeping
-- its old conflict identity would let it overwrite a qualified failure with an
-- old-policy pass while retaining the newer policy tag. Refuse that writer.
BEGIN;
ALTER TABLE gates.gate_evaluations
    DROP CONSTRAINT gate_evaluations_run_id_gate_target_sha_key;
ALTER TABLE gates.gate_evaluations
    ADD CONSTRAINT gate_evaluations_run_gate_revisions_key
    UNIQUE (run_id, gate, target_sha, policy_sha);
COMMIT;
