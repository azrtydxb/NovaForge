-- Recovery scans unresolved identifiers for one immutable operator target;
-- retained settled evidence must not make each bounded page scan all history.
CREATE INDEX sandbox_invocations_recovery ON gates.sandbox_invocations
    (target, namespace, org_id, id) WHERE NOT released;
