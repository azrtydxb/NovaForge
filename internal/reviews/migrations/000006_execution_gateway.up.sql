-- Only identity and receipt metadata: never store prompts, response bodies or keys.
ALTER TABLE review_executions
 ADD COLUMN owner_url text,
 ADD COLUMN request_digest text,
 ADD COLUMN last_checked_at timestamptz,
 ADD COLUMN receipt_version integer,
 ADD COLUMN upstream_status integer,
 ADD COLUMN completed_at timestamptz,
 ADD CONSTRAINT execution_binding CHECK (
  (owner_url IS NULL AND request_digest IS NULL) OR
  (owner_url IS NOT NULL AND request_digest IS NOT NULL AND request_digest ~ '^[0-9a-f]{64}$')),
 ADD CONSTRAINT execution_receipt CHECK (
  termination_evidence <> 'gateway_receipt' OR
  (terminated_at IS NOT NULL AND owner_url IS NOT NULL AND request_digest IS NOT NULL
   AND receipt_version IS NOT NULL AND receipt_version = 1
   AND upstream_status IS NOT NULL AND upstream_status = 200 AND completed_at IS NOT NULL));
CREATE INDEX ON review_executions(org_id,last_checked_at,created_at,attempt_id)
 WHERE terminated_at IS NULL AND owner_url IS NOT NULL;

-- Binding is single assignment, including for a duplicate transport invocation.
-- Existing unbound/legacy orphans remain held; absence of a receipt is not proof.
CREATE FUNCTION protect_execution_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN
  RAISE EXCEPTION 'execution obligations cannot be deleted';
 END IF;
 IF (NEW.attempt_id,NEW.org_id,NEW.request_id,NEW.lease_id) IS DISTINCT FROM
    (OLD.attempt_id,OLD.org_id,OLD.request_id,OLD.lease_id) OR
    (OLD.owner_url IS NOT NULL AND (NEW.owner_url,NEW.request_digest) IS DISTINCT FROM (OLD.owner_url,OLD.request_digest)) OR
    (OLD.terminated_at IS NOT NULL AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'execution identity, binding and completed receipts are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER execution_binding_immutable BEFORE UPDATE OR DELETE ON review_executions
 FOR EACH ROW EXECUTE FUNCTION protect_execution_binding();
