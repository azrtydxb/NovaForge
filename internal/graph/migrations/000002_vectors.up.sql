-- Installed into public (not the graph schema, which is this migration's
-- search_path) so the vector type resolves from any connection's default
-- search_path, not only one pinned to graph.
CREATE EXTENSION IF NOT EXISTS vector SCHEMA public;

CREATE TABLE IF NOT EXISTS code_chunks (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    path text NOT NULL,
    start_line int NOT NULL,
    end_line int NOT NULL,
    text text NOT NULL,
    embedding public.vector(768) NOT NULL
);

CREATE INDEX ON code_chunks USING hnsw (embedding public.vector_cosine_ops);

CREATE INDEX ON code_chunks (org_id, repo_id, path);
