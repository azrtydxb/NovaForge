CREATE TABLE IF NOT EXISTS retention_policies (
    org_id uuid PRIMARY KEY,
    log_days int NOT NULL DEFAULT 90 CHECK (log_days >= 0),
    evidence_days int NOT NULL DEFAULT 0 CHECK (evidence_days >= 0)
);
