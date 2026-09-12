-- Artifact paths are declared per job so the runner knows what to keep.
ALTER TABLE ci.workflow_jobs
  ADD COLUMN IF NOT EXISTS artifact_paths text[] NOT NULL DEFAULT '{}';
