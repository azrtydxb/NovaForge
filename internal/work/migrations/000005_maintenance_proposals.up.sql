-- maintenance_proposals is the swarm-adjacent maintenance proposer's
-- (internal/maintenance) idempotency and lifecycle ledger: it records
-- which (org, repo, finding fingerprint) triples have already been raised
-- as a Work Item, so proposing the identical finding twice creates one
-- Work Item, not two, and so a finding that stops reproducing can be
-- traced back to the Work Item it should close.
CREATE TABLE IF NOT EXISTS maintenance_proposals (
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    fingerprint text NOT NULL,
    work_item_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    resolved_at timestamptz,
    PRIMARY KEY (org_id, repo_id, fingerprint)
);

CREATE INDEX ON maintenance_proposals (org_id, repo_id, resolved_at);
