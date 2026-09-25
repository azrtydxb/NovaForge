CREATE TABLE capability_issuances (
 id uuid PRIMARY KEY,
 org_id uuid NOT NULL,
 issuer_id uuid NOT NULL,
 issuer_kind text NOT NULL,
 run_id uuid NOT NULL,
 intent jsonb NOT NULL,
 cancelled boolean NOT NULL DEFAULT false
);
