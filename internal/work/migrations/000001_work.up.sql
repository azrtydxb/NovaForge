CREATE TABLE IF NOT EXISTS work_items (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    seq bigint NOT NULL,
    key text NOT NULL,
    type text NOT NULL CHECK (type IN (
        'feature', 'bug', 'refactor', 'security', 'tech_debt', 'research',
        'architecture', 'upgrade', 'incident', 'documentation'
    )),
    goal text NOT NULL,
    acceptance text[] NOT NULL DEFAULT '{}',
    constraints text[] NOT NULL DEFAULT '{}',
    required_gates text[] NOT NULL DEFAULT '{}',
    assignee_id uuid,
    assignee_kind text CHECK (assignee_kind IN ('user', 'agent')),
    state text NOT NULL DEFAULT 'open' CHECK (state IN (
        'open', 'planning', 'in_progress', 'review', 'done', 'blocked'
    )),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, key),
    UNIQUE (org_id, seq)
);

CREATE INDEX ON work_items (org_id, repo_id, state);
