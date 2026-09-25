CREATE TABLE secrets.credential_scopes (
 org_id uuid NOT NULL,
 run_id uuid NOT NULL,
 closed boolean NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,run_id)
);
CREATE TABLE secrets.credential_attempts (
 org_id uuid NOT NULL,
 run_id uuid NOT NULL,
 attempt_id uuid NOT NULL,
 closed boolean NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,run_id,attempt_id),
 FOREIGN KEY(org_id,run_id) REFERENCES secrets.credential_scopes(org_id,run_id)
);
ALTER TABLE secrets.secret_leases
 ADD COLUMN attempt_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
 ADD COLUMN revocation_requested boolean NOT NULL DEFAULT false,
 ADD COLUMN revocation_error text NOT NULL DEFAULT '';
CREATE INDEX ON secrets.secret_leases(org_id,run_id,attempt_id) WHERE revoked_at IS NULL;
