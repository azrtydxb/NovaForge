-- Historical target_sha means source SHA. Policy revision is separate and
-- legacy rows cannot authorize a merge against a newly resolved target.
ALTER TABLE gates.gate_evaluations ADD COLUMN policy_sha text NOT NULL DEFAULT '';
