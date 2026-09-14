-- An agent job runs as an Agent Run, and an Agent Run must have a human
-- answerable for it: the member whose push or request scheduled the run.
ALTER TABLE ci.workflow_runs ADD COLUMN IF NOT EXISTS triggered_by uuid;
-- The Agent Run executing an agent job, and the Work Item it was briefed with,
-- so the job's status can follow the run's and a person can find both.
ALTER TABLE ci.workflow_jobs
  ADD COLUMN IF NOT EXISTS agent_run_id uuid,
  ADD COLUMN IF NOT EXISTS work_item_key text;
