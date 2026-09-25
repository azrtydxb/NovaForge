CREATE TABLE tool_events (
    sequence bigserial PRIMARY KEY,
    org_id uuid NOT NULL,
    run_id uuid NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    call_id uuid NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    tool text NOT NULL,
    outcome text NOT NULL,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    published boolean NOT NULL DEFAULT false,
    UNIQUE(call_id, outcome)
);
CREATE INDEX ON tool_events (org_id,run_id,sequence);
CREATE INDEX ON tool_events (sequence) WHERE NOT published;
