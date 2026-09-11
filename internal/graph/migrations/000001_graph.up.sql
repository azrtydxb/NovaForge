CREATE TABLE IF NOT EXISTS graph_nodes (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid,
    kind text NOT NULL CHECK (kind IN (
        'symbol', 'file', 'service', 'api', 'schema', 'test', 'work_item',
        'commit', 'adr', 'deployment', 'owner', 'incident'
    )),
    key text NOT NULL,
    attrs jsonb NOT NULL DEFAULT '{}',
    UNIQUE (org_id, kind, key)
);

CREATE TABLE IF NOT EXISTS graph_edges (
    from_id uuid NOT NULL REFERENCES graph_nodes(id) ON DELETE CASCADE,
    to_id uuid NOT NULL REFERENCES graph_nodes(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN (
        'depends_on', 'called_by', 'tested_by', 'owned_by', 'implements',
        'deployed_as', 'changed_by'
    )),
    PRIMARY KEY (from_id, to_id, kind)
);

CREATE INDEX ON graph_edges (to_id, kind);

CREATE INDEX ON graph_nodes (org_id, repo_id, kind);
