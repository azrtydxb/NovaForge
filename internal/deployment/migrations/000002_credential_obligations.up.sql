-- Registered in the lock-owning start transaction, before any issuer access.
-- Restrictive FK prevents lifecycle deletion from silently discarding cleanup.
CREATE TABLE credential_obligations (
    org_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    attempt integer NOT NULL,
    provider_binding text NOT NULL,
    phase text NOT NULL DEFAULT 'preparing' CHECK (phase IN ('preparing', 'dispatching')),
    resolved_at timestamptz,
    PRIMARY KEY (org_id, operation_id, attempt),
    FOREIGN KEY (org_id, operation_id, attempt) REFERENCES attempts(org_id, operation_id, number)
);

CREATE FUNCTION refuse_pending_credential_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.resolved_at IS NULL THEN
        RAISE EXCEPTION 'deployment credential cleanup is pending';
    END IF;
    RETURN OLD;
END
$$;
CREATE TRIGGER retain_pending_credential BEFORE DELETE ON credential_obligations
    FOR EACH ROW EXECUTE FUNCTION refuse_pending_credential_delete();
