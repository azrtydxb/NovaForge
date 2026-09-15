ALTER TABLE ci.workflow_jobs
  DROP COLUMN IF EXISTS not_before,
  DROP COLUMN IF EXISTS environment,
  DROP COLUMN IF EXISTS secrets;
