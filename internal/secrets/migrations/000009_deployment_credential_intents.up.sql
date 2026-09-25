-- Fences survive operation/organization deletion and contain no token material.
CREATE TABLE secrets.deployment_credential_intents (
 org_id uuid NOT NULL,
 operation_id uuid NOT NULL,
 attempt_id uuid NOT NULL,
 intent jsonb NOT NULL,
 closed boolean NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,operation_id,attempt_id)
);
