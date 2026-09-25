DROP INDEX ci.runner_log_seal_queue;
ALTER TABLE ci.workflow_jobs DROP COLUMN log_seal_attempted_at;
