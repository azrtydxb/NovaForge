-- Refuse rollback while artifact evidence exists, rather than deleting history.
ALTER TABLE graph_nodes DROP CONSTRAINT graph_nodes_kind_check;
ALTER TABLE graph_nodes ADD CONSTRAINT graph_nodes_kind_check CHECK (kind IN (
    'symbol', 'file', 'service', 'api', 'schema', 'test', 'work_item',
    'commit', 'adr', 'deployment', 'owner', 'incident', 'package'
));
