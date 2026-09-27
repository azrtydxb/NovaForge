DROP TRIGGER transfer_blob_ownership ON repositories;
DROP FUNCTION transfer_blob_ownership();
DROP TRIGGER release_cleanup ON release_assets;
DROP TRIGGER lfs_cleanup ON lfs_objects;
DROP FUNCTION enqueue_blob_cleanup();
DROP TABLE blob_cleanup;
DROP INDEX release_assets_blob_key;
ALTER TABLE lfs_objects DROP COLUMN blob_key;
