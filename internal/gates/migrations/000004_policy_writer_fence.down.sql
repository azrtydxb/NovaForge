-- Downgrading must not relabel qualified evidence as a legacy evaluation or
-- discard one policy's result to make the old uniqueness constraint fit.
BEGIN;
LOCK TABLE gates.gate_evaluations IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM gates.gate_evaluations WHERE policy_sha <> '') THEN
        RAISE EXCEPTION 'cannot downgrade while policy-qualified gate evidence exists';
    END IF;
END $$;
ALTER TABLE gates.gate_evaluations
    DROP CONSTRAINT gate_evaluations_run_gate_revisions_key;
ALTER TABLE gates.gate_evaluations
    ADD CONSTRAINT gate_evaluations_run_id_gate_target_sha_key
    UNIQUE (run_id, gate, target_sha);
COMMIT;
