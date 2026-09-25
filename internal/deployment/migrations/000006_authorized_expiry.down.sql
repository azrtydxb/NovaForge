ALTER TABLE deployment.materializations DROP COLUMN issued_expires_at;
ALTER TABLE deployment.attempts DROP COLUMN credential_expires_at;
ALTER TABLE deployment.attempts DROP COLUMN authorized_until;
ALTER TABLE deployment.operations DROP COLUMN authorized_until;
ALTER TABLE deployment.operations DROP COLUMN effective_until;
