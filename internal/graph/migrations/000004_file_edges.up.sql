-- The indexer now writes the edges the graph's queries read. Two things are
-- needed for that which the schema did not have.
--
-- A package is a node in its own right: a Go file depends on the packages it
-- imports, including ones outside the repository, whose symbols the graph
-- never sees.
ALTER TABLE graph_nodes DROP CONSTRAINT IF EXISTS graph_nodes_kind_check;
ALTER TABLE graph_nodes ADD CONSTRAINT graph_nodes_kind_check CHECK (kind IN (
    'symbol', 'file', 'service', 'api', 'schema', 'test', 'work_item',
    'commit', 'adr', 'deployment', 'owner', 'incident', 'package'
));

-- A reference outlives the symbol it points at. Re-indexing a file replaces
-- its symbol nodes, and deleting a node cascades to every edge into it — so
-- an edge from a file the push did not touch would silently vanish. Each
-- file's references are kept by name, and the edges between two files are
-- re-derived from them whenever either file is indexed.
CREATE TABLE IF NOT EXISTS file_references (
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    from_path text NOT NULL,
    -- The graph_nodes key of the referencing symbol, or of the file itself
    -- when the reference sits outside any function.
    from_key text NOT NULL,
    from_kind text NOT NULL CHECK (from_kind IN ('symbol', 'file')),
    target_dir text NOT NULL,
    target_name text NOT NULL,
    target_method boolean NOT NULL,
    edge_kind text NOT NULL CHECK (edge_kind IN ('depends_on', 'tested_by'))
);

CREATE INDEX IF NOT EXISTS file_references_from_idx ON file_references (org_id, repo_id, from_path);
CREATE INDEX IF NOT EXISTS file_references_target_idx ON file_references (org_id, repo_id, target_dir, target_name);
CREATE INDEX IF NOT EXISTS graph_nodes_key_idx ON graph_nodes (org_id, key);
