-- Persist physical keys across organization transfers and record cleanup before
-- accepting bytes. The queue deliberately has no cascading repository FK.
ALTER TABLE lfs_objects ADD COLUMN blob_key text;
UPDATE lfs_objects SET blob_key = 'org/' || org_id || '/repo/' || repo_id || '/lfs/' || oid;
ALTER TABLE lfs_objects ALTER COLUMN blob_key SET NOT NULL;
CREATE INDEX lfs_objects_blob_key ON lfs_objects(blob_key);
CREATE INDEX release_assets_blob_key ON release_assets(blob_key);
CREATE TABLE blob_cleanup (
    blob_key text PRIMARY KEY,
    org_id uuid NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT ''
);
CREATE INDEX blob_cleanup_ready ON blob_cleanup(available_at);
CREATE FUNCTION enqueue_blob_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO gitplatform.blob_cleanup(blob_key, org_id)
    VALUES (OLD.blob_key, split_part(OLD.blob_key, '/', 2)::uuid)
    ON CONFLICT(blob_key) DO NOTHING;
    RETURN OLD;
END $$;
CREATE TRIGGER lfs_cleanup AFTER DELETE ON lfs_objects FOR EACH ROW EXECUTE FUNCTION enqueue_blob_cleanup();
CREATE TRIGGER release_cleanup AFTER DELETE ON release_assets FOR EACH ROW EXECUTE FUNCTION enqueue_blob_cleanup();
-- Denormalized authorization follows ownership; physical keys do not change.
CREATE FUNCTION transfer_blob_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE gitplatform.lfs_objects SET org_id = NEW.org_id WHERE repo_id = NEW.id;
    UPDATE gitplatform.releases SET org_id = NEW.org_id WHERE repo_id = NEW.id;
    RETURN NEW;
END $$;
CREATE TRIGGER transfer_blob_ownership AFTER UPDATE OF org_id ON repositories
FOR EACH ROW WHEN (OLD.org_id IS DISTINCT FROM NEW.org_id) EXECUTE FUNCTION transfer_blob_ownership();
