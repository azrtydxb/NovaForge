ALTER TABLE secrets.secret_leases
 DROP COLUMN provider_ready,
 DROP COLUMN issued_ciphertext,
 DROP COLUMN provider_endpoint,
 DROP COLUMN provider_lease_id;
