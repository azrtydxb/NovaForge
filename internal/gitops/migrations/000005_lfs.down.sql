-- The payloads themselves live in object storage and are not removed here: a
-- migration rollback undoes schema, and deleting a repository's binaries because
-- someone stepped a migration back would be unrecoverable.
DROP TABLE IF EXISTS lfs_objects;
