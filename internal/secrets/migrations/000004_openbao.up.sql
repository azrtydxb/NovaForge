-- Empty metadata identifies legacy static leases, which cannot be redeemed
-- as expiring credentials. Never infer a provider lease for existing material.
ALTER TABLE secrets.secret_leases
 ADD COLUMN provider_lease_id text NOT NULL DEFAULT '',
 ADD COLUMN provider_endpoint text NOT NULL DEFAULT '',
 ADD COLUMN issued_ciphertext bytea,
 ADD COLUMN provider_ready boolean NOT NULL DEFAULT false;
