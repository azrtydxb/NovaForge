-- An observed artifact is not a repository, service, or inferred source commit.
ALTER TABLE graph_nodes DROP CONSTRAINT graph_nodes_kind_check;
ALTER TABLE graph_nodes ADD CONSTRAINT graph_nodes_kind_check CHECK (kind IN (
    'symbol', 'file', 'service', 'api', 'schema', 'test', 'work_item',
    'commit', 'adr', 'deployment', 'owner', 'incident', 'package', 'artifact'
));
