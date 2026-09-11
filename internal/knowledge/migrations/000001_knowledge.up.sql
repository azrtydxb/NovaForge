-- vector is installed into public (not the knowledge schema, which is this
-- migration's search_path) so the type resolves from any connection's
-- default search_path, not only one pinned to knowledge. IF NOT EXISTS
-- makes this a no-op when another service's migration already installed it.
CREATE EXTENSION IF NOT EXISTS vector SCHEMA public;

CREATE TABLE IF NOT EXISTS knowledge_entries (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    key text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('decision', 'pattern', 'incident', 'correction', 'operational')),
    title text NOT NULL,
    body text NOT NULL,
    source_run_id uuid,
    superseded_by uuid REFERENCES knowledge_entries(id),
    embedding public.vector(768),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, repo_id, key)
);

CREATE INDEX ON knowledge_entries USING hnsw (embedding public.vector_cosine_ops);

CREATE INDEX ON knowledge_entries (org_id, repo_id, superseded_by);
