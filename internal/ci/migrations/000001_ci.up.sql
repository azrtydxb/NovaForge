CREATE TABLE IF NOT EXISTS workflow_runs (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    commit_sha text NOT NULL,
    ref text NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'success', 'failure', 'cancelled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, commit_sha, ref)
);

CREATE TABLE IF NOT EXISTS workflow_jobs (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    name text NOT NULL,
    needs text[] NOT NULL DEFAULT '{}',
    run_cmd text,
    agent_role text,
    image text,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'success', 'failure', 'cancelled')),
    runner_id uuid,
    detail text,
    started_at timestamptz,
    finished_at timestamptz,
    UNIQUE (run_id, name)
);

CREATE TABLE IF NOT EXISTS runners (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    name text NOT NULL,
    labels text[] NOT NULL DEFAULT '{}',
    token_hash bytea NOT NULL UNIQUE,
    last_seen_at timestamptz
);

CREATE INDEX ON workflow_runs (org_id, repo_id);
CREATE INDEX ON workflow_jobs (run_id, status);
CREATE INDEX ON runners (org_id);
