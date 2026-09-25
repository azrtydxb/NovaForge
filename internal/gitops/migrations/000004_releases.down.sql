-- release_assets first: it references releases, and dropping the referenced
-- table first leaves the dependent one behind on a partial rollback.
DROP TABLE IF EXISTS release_assets;
DROP TABLE IF EXISTS releases;
