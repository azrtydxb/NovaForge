ALTER TABLE deployment.operations ADD COLUMN authorized_until timestamptz;
ALTER TABLE deployment.operations ADD COLUMN effective_until timestamptz;
ALTER TABLE deployment.attempts ADD COLUMN authorized_until timestamptz;
ALTER TABLE deployment.attempts ADD COLUMN credential_expires_at timestamptz;
-- Legacy attempts need their original issuer request for cleanup, not renewed
-- authorization. Legacy agent operations without authorized_until cannot execute.
UPDATE deployment.attempts SET credential_expires_at = started_at + interval '10 minutes';
ALTER TABLE deployment.materializations ADD COLUMN issued_expires_at timestamptz;
-- Only confirmed old materialization proves the old exact-expiry validation.
-- Unknown creates stay unknown; their cleanup request remains unchanged.
UPDATE deployment.materializations SET issued_expires_at = (intent->>'ExpiresAt')::timestamptz WHERE phase='materialized';
