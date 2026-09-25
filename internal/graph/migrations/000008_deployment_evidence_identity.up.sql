-- Only opaque identity survives graph purge. This is an anti-retarget fence,
-- not retained deployment payload, artifact content, or operational evidence.
CREATE TABLE deployment_evidence_identities (
    org_id uuid NOT NULL,
    evidence_id text NOT NULL,
    repo_id uuid NOT NULL,
    PRIMARY KEY (org_id, evidence_id)
);

-- Preserve already-observed identities when upgrading before their first purge.
INSERT INTO deployment_evidence_identities (org_id, evidence_id, repo_id)
SELECT org_id, attrs->>'evidence_id', repo_id FROM graph_nodes
WHERE kind='deployment' AND attrs->>'provenance'='observed'
  AND attrs->>'producer'='deployment' AND attrs->>'evidence_id' IS NOT NULL
  AND repo_id IS NOT NULL;
