ALTER TABLE secrets.secret_leases DROP COLUMN attempt_id, DROP COLUMN revocation_requested, DROP COLUMN revocation_error;
DROP TABLE secrets.credential_attempts;
DROP TABLE secrets.credential_scopes;
