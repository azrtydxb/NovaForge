DROP INDEX credential_cleanup_pending;
ALTER TABLE credential_obligations DROP COLUMN cleanup_attempted_at;
