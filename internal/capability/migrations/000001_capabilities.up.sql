CREATE TABLE IF NOT EXISTS capability_grants (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('user', 'agent')),
    repo_read boolean NOT NULL DEFAULT false,
    write_branch text NOT NULL DEFAULT '',
    secrets_prod boolean NOT NULL DEFAULT false,
    deploy_staging boolean NOT NULL DEFAULT false,
    deploy_prod boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
