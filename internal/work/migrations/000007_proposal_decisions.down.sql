DROP INDEX IF EXISTS maintenance_proposals_work_item;
ALTER TABLE maintenance_proposals
    DROP CONSTRAINT IF EXISTS maintenance_proposals_dismissal_has_reason,
    DROP CONSTRAINT IF EXISTS maintenance_proposals_decision_complete,
    DROP COLUMN IF EXISTS dismiss_reason,
    DROP COLUMN IF EXISTS decided_at,
    DROP COLUMN IF EXISTS decided_by,
    DROP COLUMN IF EXISTS decision;
