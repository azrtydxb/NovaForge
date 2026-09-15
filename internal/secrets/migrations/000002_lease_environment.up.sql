-- A secret name can exist in both environments. A lease records which one it
-- was issued for, so redeeming it decrypts that value and not whichever the
-- lookup happens to prefer.
ALTER TABLE secrets.secret_leases ADD COLUMN IF NOT EXISTS environment text NOT NULL DEFAULT '';
