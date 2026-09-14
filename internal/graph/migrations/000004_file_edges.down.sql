DROP INDEX IF EXISTS graph_nodes_key_idx;
DROP TABLE IF EXISTS file_references;
DELETE FROM graph_nodes WHERE kind = 'package';
ALTER TABLE graph_nodes DROP CONSTRAINT IF EXISTS graph_nodes_kind_check;
ALTER TABLE graph_nodes ADD CONSTRAINT graph_nodes_kind_check CHECK (kind IN (
    'symbol', 'file', 'service', 'api', 'schema', 'test', 'work_item',
    'commit', 'adr', 'deployment', 'owner', 'incident'
));
