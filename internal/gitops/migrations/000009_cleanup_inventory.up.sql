-- Inventory opaque IDs before loading each owner's physical key.
ALTER TABLE blob_cleanup ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid();
CREATE UNIQUE INDEX blob_cleanup_id ON blob_cleanup(id);
