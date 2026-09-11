CREATE TABLE IF NOT EXISTS approval_requests (
    id           uuid PRIMARY KEY,
    org_id       uuid NOT NULL,
    run_id       uuid NOT NULL,
    action       text NOT NULL,
    detail       jsonb NOT NULL,
    decision     text NOT NULL DEFAULT 'pending',
    decided_by   uuid,
    decided_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ON approval_requests (org_id, run_id);
