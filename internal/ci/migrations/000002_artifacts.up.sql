CREATE TABLE IF NOT EXISTS artifacts (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    job_id uuid NOT NULL REFERENCES workflow_jobs(id) ON DELETE CASCADE,
    name text NOT NULL,
    size_bytes bigint NOT NULL,
    object_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (job_id, name)
);

CREATE INDEX ON artifacts (org_id, job_id);
