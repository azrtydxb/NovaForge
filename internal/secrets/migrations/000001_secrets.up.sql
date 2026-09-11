CREATE TABLE IF NOT EXISTS secret_values (
    org_id      uuid NOT NULL,
    name        text NOT NULL,
    environment text NOT NULL CHECK (environment IN ('staging', 'production')),
    ciphertext  bytea NOT NULL,
    PRIMARY KEY (org_id, name, environment)
);

CREATE TABLE IF NOT EXISTS secret_leases (
    id              uuid PRIMARY KEY,
    org_id          uuid NOT NULL,
    run_id          uuid NOT NULL,
    name            text NOT NULL,
    token_hash      bytea NOT NULL UNIQUE,
    expires_at      timestamptz NOT NULL,
    revoked_at      timestamptz,
    redeemed_count  int NOT NULL DEFAULT 0
);

CREATE INDEX ON secret_leases (org_id, run_id);
