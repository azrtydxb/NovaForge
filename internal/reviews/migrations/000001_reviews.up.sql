CREATE TABLE IF NOT EXISTS runs (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    work_item_id uuid,
    number int NOT NULL,
    title text NOT NULL,
    source_ref text NOT NULL,
    target_ref text NOT NULL,
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'merged', 'closed')),
    author_id uuid NOT NULL,
    author_kind text NOT NULL CHECK (author_kind IN ('user', 'agent')),
    agent_name text,
    model_name text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, number)
);

CREATE TABLE IF NOT EXISTS run_plan_steps (
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    ordinal int NOT NULL,
    text text NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    PRIMARY KEY (run_id, ordinal)
);

CREATE TABLE IF NOT EXISTS run_proof (
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    gate text NOT NULL,
    status text NOT NULL,
    detail text NOT NULL DEFAULT '',
    recorded_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, gate)
);

CREATE TABLE IF NOT EXISTS run_comments (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    author_id uuid NOT NULL,
    author_kind text NOT NULL CHECK (author_kind IN ('user', 'agent')),
    body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS run_reviews (
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    reviewer_id uuid NOT NULL,
    reviewer_kind text NOT NULL CHECK (reviewer_kind IN ('user', 'agent')),
    verdict text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, reviewer_id)
);

CREATE INDEX ON runs (org_id, repo_id, state);
