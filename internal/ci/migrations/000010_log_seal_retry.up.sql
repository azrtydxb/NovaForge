-- Claims are durable and expire after process loss. Older failures rotate
-- behind never-attempted work rather than occupying every oldest-100 batch.
ALTER TABLE ci.workflow_jobs ADD COLUMN log_seal_attempted_at timestamptz;
CREATE INDEX runner_log_seal_queue ON ci.workflow_jobs(log_seal_attempted_at,finished_at,id) WHERE log_seal_pending;
