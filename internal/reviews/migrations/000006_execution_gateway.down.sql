DROP TRIGGER execution_binding_immutable ON review_executions;
DROP FUNCTION protect_execution_binding();
ALTER TABLE review_executions
 DROP COLUMN owner_url,
 DROP COLUMN request_digest,
 DROP COLUMN last_checked_at,
 DROP COLUMN receipt_version,
 DROP COLUMN upstream_status,
 DROP COLUMN completed_at;
