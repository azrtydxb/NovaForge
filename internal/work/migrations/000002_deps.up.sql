ALTER TABLE work_items ADD COLUMN parent_id uuid REFERENCES work_items(id);

CREATE TABLE IF NOT EXISTS work_item_deps (
    blocked_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    blocker_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    PRIMARY KEY (blocked_id, blocker_id),
    CHECK (blocked_id <> blocker_id)
);

CREATE INDEX ON work_item_deps (blocker_id);
CREATE INDEX ON work_items (parent_id);
