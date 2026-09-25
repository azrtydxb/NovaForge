ALTER TABLE agent_runs ADD COLUMN completion jsonb,
    ADD COLUMN tokens_available boolean NOT NULL DEFAULT false,
    ADD COLUMN cost_available boolean NOT NULL DEFAULT false;
