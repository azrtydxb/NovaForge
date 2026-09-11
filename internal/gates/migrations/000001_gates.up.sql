CREATE TABLE IF NOT EXISTS gate_evaluations (
    id           uuid PRIMARY KEY,
    org_id       uuid NOT NULL,
    run_id       uuid NOT NULL,
    gate         text NOT NULL,
    status       text NOT NULL CHECK (status IN ('pass', 'fail', 'error', 'skipped')),
    detail       text NOT NULL DEFAULT '',
    target_sha   text NOT NULL,
    evaluated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, gate, target_sha)
);

CREATE INDEX ON gate_evaluations (org_id, run_id);
