-- Each accepted stream owns a server-generated incarnation. NULL means no
-- currently admitted stream, including legacy registrations after upgrade.
ALTER TABLE ci.runners ADD COLUMN connection_id uuid;
ALTER TABLE ci.workflow_jobs ADD COLUMN connection_id uuid;
