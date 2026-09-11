CREATE TABLE IF NOT EXISTS tool_calls (
    id         uuid PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    org_id     uuid NOT NULL,
    tool       text NOT NULL,
    args_json  jsonb NOT NULL,
    outcome    text NOT NULL DEFAULT 'pending',
    error      text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz
);

CREATE INDEX ON tool_calls (run_id, started_at);
