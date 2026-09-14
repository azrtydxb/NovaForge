ALTER TABLE ci.workflow_jobs DROP COLUMN IF EXISTS work_item_key, DROP COLUMN IF EXISTS agent_run_id;
ALTER TABLE ci.workflow_runs DROP COLUMN IF EXISTS triggered_by;
