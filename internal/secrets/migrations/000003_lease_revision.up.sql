-- Pin encrypted material, not its plaintext: a replaced secret cannot be
-- silently substituted into a lease authorized against an earlier revision.
-- Existing leases have no digest and must be reissued, never guessed.
ALTER TABLE secrets.secret_leases ADD COLUMN secret_digest bytea;
