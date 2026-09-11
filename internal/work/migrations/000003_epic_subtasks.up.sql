-- epic_subtasks is the swarm planner's idempotency ledger: it records which
-- (epic, subtask key) pairs have already been materialised as a child work
-- item, so calling Materialise twice with the same subtask keys creates one
-- set of children, not two.
CREATE TABLE IF NOT EXISTS epic_subtasks (
    parent_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    key text NOT NULL,
    work_item_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    PRIMARY KEY (parent_id, key)
);
