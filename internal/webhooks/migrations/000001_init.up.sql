-- Webhooks: an endpoint outside the platform that is told when something happens
-- in a repository, and the record of what it answered.
--
-- These tables live in git-platform's own "gitplatform" schema, applied under
-- their own tracking table (see embed.go): a hook belongs to a repository, and
-- the repository is git-platform's. A second schema would mean either a
-- cross-schema read on every delivery or an RPC round trip per push, and the
-- foreign key below — which is what removes a deleted repository's hooks —
-- could not exist at all.
CREATE TABLE hooks (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    url text NOT NULL,
    -- The event names this hook asked for. Empty means every event: a hook
    -- registered without a filter should notify, not sit silent.
    events text[] NOT NULL DEFAULT '{}',
    -- The shared secret, AES-GCM ciphertext under the service KEK, exactly as
    -- secrets.secret_values holds a stored credential. NULL is a hook with no
    -- secret, which is delivered unsigned; it is never an empty blob, so "no
    -- secret" and "a secret nobody can decrypt" cannot be confused.
    secret bytea,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Every read is "the hooks of this repository, in this organization": the
-- organization is part of the predicate on each one rather than inferred from
-- the repository, so no read can cross the boundary even if a row's org_id and
-- its repository ever disagreed.
CREATE INDEX hooks_by_repo ON hooks(org_id, repo_id);

-- One row per attempt, not per delivery: "it failed twice and then succeeded" is
-- the history worth keeping, and a single row updated in place would throw away
-- every attempt but the last.
CREATE TABLE hook_deliveries (
    id uuid PRIMARY KEY,
    hook_id uuid NOT NULL REFERENCES hooks(id) ON DELETE CASCADE,
    event text NOT NULL,
    -- 0 when the request never got an HTTP response at all (DNS failure,
    -- connection refused, timeout); error then says why.
    status_code integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    attempt integer NOT NULL,
    at timestamptz NOT NULL DEFAULT now()
);

-- The delivery history is always read newest-first for one hook.
CREATE INDEX hook_deliveries_by_hook ON hook_deliveries(hook_id, at DESC);
