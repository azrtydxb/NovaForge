-- A run's cost limit was stored and never compared with anything: nothing
-- priced a token, so no run ever accrued cost and the limit could not trip.
-- Cost is now accrued from the deployment's configured token price and
-- persisted beside tokens_used.
--
-- end_reason records why a run that did not succeed ended — which budget limit
-- stopped it, or what failed — so "over_budget" is answerable afterwards
-- without reading the audit log.
ALTER TABLE agent_runs
    ADD COLUMN IF NOT EXISTS cost_used_micros bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS end_reason text NOT NULL DEFAULT '';
