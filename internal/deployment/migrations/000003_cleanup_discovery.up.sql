ALTER TABLE credential_obligations ADD COLUMN cleanup_attempted_at timestamptz;
CREATE INDEX credential_cleanup_pending ON credential_obligations
    (cleanup_attempted_at ASC NULLS FIRST, org_id, operation_id, attempt)
    WHERE resolved_at IS NULL;
