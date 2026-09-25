-- Success and its publication obligation commit together. Event identity refers
-- to the delivery attempt, not a later read-only observation attempt.
CREATE TABLE success_outbox (
    org_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    execute_attempt integer NOT NULL,
    payload jsonb NOT NULL,
    publish_attempted_at timestamptz,
    published_at timestamptz,
    PRIMARY KEY (org_id, operation_id, execute_attempt),
    FOREIGN KEY (org_id, operation_id, execute_attempt)
        REFERENCES attempts(org_id, operation_id, number)
);
CREATE INDEX ON success_outbox (publish_attempted_at ASC NULLS FIRST, org_id, operation_id, execute_attempt)
    WHERE published_at IS NULL;
