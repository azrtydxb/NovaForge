DROP TRIGGER fence_run_credentials ON ci.workflow_runs;
DROP FUNCTION ci.fence_run_credentials();
DROP TRIGGER fence_job_credentials ON ci.workflow_jobs;
DROP FUNCTION ci.fence_job_credentials();
DROP TABLE ci.credential_cleanup;
