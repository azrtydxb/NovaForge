-- Admission and terminal receipts share the runner-row fence. Redis/object
-- storage are projections, never a second authority for accepting runner work.
CREATE TABLE ci.runner_log_lines (
 job_id uuid NOT NULL REFERENCES ci.workflow_jobs(id) ON DELETE CASCADE,
 sequence bigint NOT NULL CHECK(sequence>0),
 line text NOT NULL,
 content_sha256 bytea NOT NULL,
 PRIMARY KEY(job_id,sequence)
);
ALTER TABLE ci.workflow_jobs ADD COLUMN journal_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE ci.workflow_jobs ADD COLUMN log_sequence bigint NOT NULL DEFAULT 0;
ALTER TABLE ci.workflow_jobs ADD COLUMN terminal_receipt bytea;
ALTER TABLE ci.workflow_jobs ADD COLUMN log_seal_pending boolean NOT NULL DEFAULT false;
ALTER TABLE ci.workflow_jobs ADD COLUMN logs_retired boolean NOT NULL DEFAULT false;
ALTER TABLE ci.artifacts ADD COLUMN content_sha256 bytea;
