-- The swarm scheduler needs to know which agent role a materialised
-- subtask was assigned to, so it can route a ready subtask to an enabled
-- agent for that role. work_items itself carries no such column (agent
-- role is a swarm planning concept, not a general Work Item field), so it
-- lives alongside the (parent_id, key) idempotency ledger it was created
-- with.
ALTER TABLE epic_subtasks ADD COLUMN agent_role text NOT NULL DEFAULT '';
