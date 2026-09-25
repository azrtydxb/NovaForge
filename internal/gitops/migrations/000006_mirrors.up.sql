-- A mirror is a repository whose history belongs to somewhere else. Importing
-- one is the migration path onto this platform; keeping it mirrored is the
-- transition period where both sides still exist.
--
-- One row per repository, so repo_id is the primary key: two mirrors of one
-- repository would each claim to own its refs and would overwrite each other on
-- every refresh.
CREATE TABLE repository_mirrors (
    repo_id uuid PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    -- org_id is denormalized here as it is on releases: every query in this
    -- package carries an organization predicate taken from the caller's scope,
    -- and requiring a join to repositories to apply it is how such a predicate
    -- ends up quietly omitted.
    org_id uuid NOT NULL,
    -- The upstream URL, and never a credential inside it. Git's own idiom is
    -- https://user:token@host/..., which puts a token for somebody else's host
    -- into a table, into every log line that prints the URL, and into the
    -- process table of every fetch. The credential lives in the column below
    -- instead, and is handed to git through its environment.
    remote text NOT NULL,
    -- AES-GCM ciphertext under the service KEK, exactly as secrets.secret_values
    -- and hooks.secret hold a stored credential. NULL is an anonymous mirror,
    -- never an empty blob, so "no credential" and "a credential nobody can
    -- decrypt" cannot be confused.
    credential bytea,
    -- How long to leave upstream alone between refreshes. 0 means every pass of
    -- the mirrorer, which is what a test wants and an operator does not.
    interval_seconds integer NOT NULL DEFAULT 3600 CHECK (interval_seconds >= 0),
    -- NULL means never synced since the remote was last changed, which is what
    -- makes a mirror due immediately after someone repoints it: what was fetched
    -- before came from a different upstream.
    last_synced_at timestamptz,
    -- Why the last refresh failed, scrubbed of anything secret, because the GUI
    -- shows it. Empty means the last refresh succeeded.
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The mirrorer's only query is "which mirrors are due", across organizations,
-- oldest first.
CREATE INDEX repository_mirrors_due ON repository_mirrors(last_synced_at NULLS FIRST);
