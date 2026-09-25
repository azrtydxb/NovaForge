ALTER TABLE ci.artifacts DROP COLUMN content_sha256;
ALTER TABLE ci.workflow_jobs DROP COLUMN journal_enabled, DROP COLUMN logs_retired, DROP COLUMN log_seal_pending, DROP COLUMN terminal_receipt, DROP COLUMN log_sequence;
DROP TABLE ci.runner_log_lines;
