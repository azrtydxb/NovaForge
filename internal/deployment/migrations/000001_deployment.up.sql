-- Canonical destination ownership is global; operation evidence remains org-scoped.
-- Ownership is deliberately retained until an operator resolves action retention.
CREATE TABLE destinations (
    destination text PRIMARY KEY,
    org_id uuid NOT NULL,
    UNIQUE (destination, org_id)
);
CREATE TABLE operations (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    run_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    actor_kind text NOT NULL CHECK (actor_kind IN ('user', 'agent')),
    target text NOT NULL,
    target_revision text NOT NULL,
    destination text NOT NULL,
    environment text NOT NULL CHECK (environment IN ('staging', 'production')),
    artifact text NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'uncertain')),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (destination, org_id) REFERENCES destinations(destination, org_id),
    UNIQUE (org_id, id)
);
CREATE INDEX ON operations (org_id, run_id);
CREATE INDEX ON operations (org_id, destination);
CREATE TABLE attempts (
    org_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    number integer NOT NULL CHECK (number > 0),
    kind text NOT NULL CHECK (kind IN ('execute', 'observe', 'recover')),
    actor_id uuid NOT NULL,
    actor_kind text NOT NULL CHECK (actor_kind IN ('user', 'agent')),
    state text NOT NULL CHECK (state IN ('running', 'succeeded', 'failed', 'uncertain')),
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    result jsonb NOT NULL DEFAULT '{}',
    error text NOT NULL DEFAULT '',
    PRIMARY KEY (org_id, operation_id, number),
    FOREIGN KEY (org_id, operation_id) REFERENCES operations(org_id, id)
);
