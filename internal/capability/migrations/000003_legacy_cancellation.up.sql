-- Cancellation evidence only: these rows do not assert historical issuance.
CREATE TABLE legacy_grant_cancellations (
 id uuid PRIMARY KEY,
 org_id uuid NOT NULL,
 run_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 branch text NOT NULL,
 cancelled_at timestamptz NOT NULL DEFAULT now()
);
