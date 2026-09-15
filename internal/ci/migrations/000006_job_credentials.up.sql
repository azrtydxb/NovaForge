-- A job's declared secrets and environment are what the broker is asked for
-- at dispatch; they were parsed from the workflow and then dropped, so no job
-- ever received a credential. not_before holds a job whose credentials could
-- not be brokered back from the claim for a while, so a blocked job neither
-- hammers the broker nor starves the jobs behind it.
ALTER TABLE ci.workflow_jobs
  ADD COLUMN IF NOT EXISTS secrets text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS environment text NOT NULL DEFAULT 'staging',
  ADD COLUMN IF NOT EXISTS not_before timestamptz;
