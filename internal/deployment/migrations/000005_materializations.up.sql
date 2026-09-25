-- This is a Kubernetes materialization fence, not a second issuer lease ledger.
-- A creating row with no observed UID cannot be cleared by Kubernetes NotFound:
-- an API request may still commit after the caller or its database session dies.
CREATE TABLE materializations (
 org_id uuid NOT NULL,
 operation_id uuid NOT NULL,
 attempt integer NOT NULL,
 intent jsonb NOT NULL,
 namespace text NOT NULL,
 secret_name text NOT NULL,
 marker uuid NOT NULL,
 closed boolean NOT NULL DEFAULT false,
 phase text NOT NULL DEFAULT 'intent' CHECK (phase IN ('intent','creating','materialized','absent')),
 secret_uid text NOT NULL DEFAULT '',
 PRIMARY KEY(org_id,operation_id,attempt),
 FOREIGN KEY(org_id,operation_id,attempt) REFERENCES attempts(org_id,operation_id,number)
);
