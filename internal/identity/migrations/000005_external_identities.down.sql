DROP TABLE IF EXISTS external_identities;
-- Fails, deliberately, while a single-sign-on user holds no email: restoring
-- the constraint would mean deleting or inventing data, and a down migration
-- must not do either silently.
ALTER TABLE users ALTER COLUMN email SET NOT NULL;
