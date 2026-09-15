ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS end_reason,
    DROP COLUMN IF EXISTS cost_used_micros;
