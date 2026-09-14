-- A maintenance proposal waits for a person's decision before anything acts
-- on it (design section 23: "wait for policy or human approval before
-- execution"). Until this migration a proposal had no decision at all: it was
-- an open Work Item indistinguishable from any other, so nothing could refuse
-- to execute one and nobody could approve one.
--
-- decision is NULL while the proposal awaits approval. decided_by is the
-- person who decided — never an agent, which must not approve work in order
-- to do it. dismiss_reason is required for a dismissal, so a declined finding
-- can be understood later rather than looking like a mistake.
ALTER TABLE maintenance_proposals
    ADD COLUMN IF NOT EXISTS decision text CHECK (decision IN ('approved', 'dismissed')),
    ADD COLUMN IF NOT EXISTS decided_by uuid,
    ADD COLUMN IF NOT EXISTS decided_at timestamptz,
    ADD COLUMN IF NOT EXISTS dismiss_reason text;

ALTER TABLE maintenance_proposals
    ADD CONSTRAINT maintenance_proposals_decision_complete CHECK (
        (decision IS NULL AND decided_by IS NULL AND decided_at IS NULL)
        OR (decision IS NOT NULL AND decided_by IS NOT NULL AND decided_at IS NOT NULL)
    ),
    ADD CONSTRAINT maintenance_proposals_dismissal_has_reason CHECK (
        decision IS DISTINCT FROM 'dismissed' OR coalesce(btrim(dismiss_reason), '') <> ''
    );

CREATE INDEX IF NOT EXISTS maintenance_proposals_work_item ON maintenance_proposals (work_item_id);
