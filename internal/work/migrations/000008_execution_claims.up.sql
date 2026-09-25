-- Admission and human intent changes serialize on this same work_items row.
-- Historical admission survives release: started intent is never editable as
-- though it were unstarted merely because a controller reopens its state.
ALTER TABLE work_items ADD COLUMN execution_claimed boolean NOT NULL DEFAULT false;
ALTER TABLE work_items ADD COLUMN active_execution_run_id uuid;
CREATE TABLE execution_claims (
    org_id uuid NOT NULL,
    run_id uuid NOT NULL,
    work_item_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    repo_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    sponsor_id uuid,
    intent bytea,
    outcome text CHECK (outcome IN ('succeeded', 'failed', 'cancelled', 'over_budget', 'admission_failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    released_at timestamptz,
    PRIMARY KEY (org_id, run_id),
    CHECK ((outcome IS NULL) = (released_at IS NULL)),
    CHECK ((intent IS NOT NULL AND sponsor_id IS NOT NULL) OR (outcome IS NOT DISTINCT FROM 'admission_failed' AND intent IS NULL AND sponsor_id IS NULL))
);
CREATE UNIQUE INDEX execution_claims_active_item ON execution_claims(org_id, work_item_id) WHERE outcome IS NULL;
