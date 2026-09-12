-- A run's budget was enforced in memory and its actual spend then discarded:
-- agentrun.Loop counted every response's tokens, compared them against the
-- limit, and returned the total to a caller that dropped it. Nobody could see
-- afterwards what an agent had cost, which is exactly the question asked when
-- deciding whether to keep giving it work.
ALTER TABLE agent_runs
    ADD COLUMN IF NOT EXISTS tokens_used bigint NOT NULL DEFAULT 0;
