CREATE TABLE semantic_snapshots (
 org_id uuid NOT NULL,
 repo_id uuid NOT NULL,
 revision text NOT NULL,
 snapshot_digest text NOT NULL,
 execution_digest text NOT NULL,
 execution_manifest jsonb NOT NULL,
 source_manifest jsonb NOT NULL,
 evidence jsonb NOT NULL,
 absence_safe boolean NOT NULL DEFAULT false CHECK (NOT absence_safe),
 PRIMARY KEY (org_id, repo_id)
);
