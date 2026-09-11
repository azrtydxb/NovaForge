CREATE TABLE IF NOT EXISTS agents (
    id         uuid PRIMARY KEY,
    org_id     uuid NOT NULL,
    name       text NOT NULL,
    role       text NOT NULL,
    model_ref  text NOT NULL,
    enabled    boolean NOT NULL DEFAULT true,
    UNIQUE (org_id, name)
);

CREATE TABLE IF NOT EXISTS agent_runs (
    id                     uuid PRIMARY KEY,
    org_id                 uuid NOT NULL,
    agent_id               uuid NOT NULL REFERENCES agents(id),
    work_item_id           uuid,
    sponsor_id             uuid NOT NULL,
    grant_id               uuid NOT NULL,
    branch                 text NOT NULL,
    state                  text NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'over_budget')),
    started_at             timestamptz,
    ended_at               timestamptz,
    wallclock_limit_seconds int NOT NULL DEFAULT 3600,
    token_limit            bigint NOT NULL DEFAULT 1000000,
    cost_limit_micros      bigint NOT NULL DEFAULT 5000000
);

CREATE INDEX IF NOT EXISTS agent_runs_org_idx ON agent_runs (org_id);

CREATE TABLE IF NOT EXISTS run_provenance (
    run_id        uuid PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
    agent_name    text NOT NULL,
    model_name    text NOT NULL,
    work_item_key text,
    run_ref       text NOT NULL,
    sponsor_name  text NOT NULL
);
