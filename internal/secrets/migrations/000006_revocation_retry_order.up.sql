ALTER TABLE secrets.secret_leases ADD COLUMN revocation_attempted_at timestamptz;
CREATE INDEX ON secrets.secret_leases(org_id,revocation_attempted_at) WHERE revocation_requested AND revoked_at IS NULL;
