-- Operator resource metadata only: no tenant identities or payloads. Configuration
-- is installed by trusted startup, never changed by an invocation request.
CREATE TABLE gates.sandbox_capacity (
    target text PRIMARY KEY,
    namespace text NOT NULL,
    ceiling integer NOT NULL CHECK (ceiling > 0),
    reserved integer NOT NULL DEFAULT 0 CHECK (reserved >= 0 AND reserved <= ceiling)
);

-- These tombstones are independent of tenant/run lifetime and are never cascaded.
CREATE TABLE gates.sandbox_org_fences (
    org_id uuid PRIMARY KEY,
    deleting boolean NOT NULL DEFAULT false
);
CREATE TABLE gates.sandbox_run_fences (
    org_id uuid NOT NULL,
    run_id uuid NOT NULL,
    PRIMARY KEY (org_id, run_id)
);

CREATE TABLE gates.sandbox_attempts (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    identity jsonb NOT NULL,
    UNIQUE (org_id, id)
);
CREATE TABLE gates.sandbox_evaluation_links (
    evaluation_id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    UNIQUE (org_id, attempt_id, evaluation_id),
    FOREIGN KEY (org_id, attempt_id) REFERENCES gates.sandbox_attempts(org_id, id)
);

CREATE TABLE gates.sandbox_invocations (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    run_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    target text NOT NULL REFERENCES gates.sandbox_capacity(target),
    namespace text NOT NULL,
    pod_name text NOT NULL,
    identity jsonb NOT NULL,
    create_claim uuid,
    pod_uid text,
    exec_claim uuid,
    terminal jsonb,
    cleanup jsonb,
    absence jsonb,
    tool_result jsonb,
    accepted_evaluation uuid,
    released boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (target, namespace, pod_name),
    FOREIGN KEY (org_id, attempt_id) REFERENCES gates.sandbox_attempts(org_id, id),
    FOREIGN KEY (org_id, attempt_id, accepted_evaluation) REFERENCES gates.sandbox_evaluation_links(org_id, attempt_id, evaluation_id),
    CHECK (pod_uid IS NULL OR (create_claim IS NOT NULL AND pod_uid <> '')),
    CHECK (exec_claim IS NULL OR pod_uid IS NOT NULL),
    CHECK (terminal IS NULL OR pod_uid IS NOT NULL),
    CHECK (cleanup IS NULL OR terminal IS NOT NULL),
    CHECK (absence IS NULL OR cleanup IS NOT NULL),
    CHECK (NOT released OR (terminal IS NOT NULL AND absence IS NOT NULL)),
    CHECK (tool_result IS NULL OR exec_claim IS NOT NULL),
    CHECK (accepted_evaluation IS NULL OR (released AND tool_result IS NOT NULL))
);
CREATE INDEX sandbox_invocations_unresolved ON gates.sandbox_invocations (org_id, run_id) WHERE NOT released;
