-- A release is a tag a team hands out, carrying files people download. CI
-- artifacts already existed and are not this: an artifact belongs to one job and
-- is evidence of a run, while a release belongs to a version and is a published
-- deliverable that outlives every run.
--
-- org_id is stored here as well as reachable through repo_id. It is denormalized
-- deliberately: every query in this package carries an organization predicate
-- taken from the caller's scope, and requiring a join to repositories to apply it
-- is how such a predicate ends up quietly omitted. It also makes the blobstore
-- key's organization segment derivable from the row that names the object.
CREATE TABLE releases (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    -- The tag must exist in the repository when the release is created; that is
    -- checked against the repository itself, since Git owns refs and no table
    -- can be made to agree with a tag someone deletes afterwards.
    tag text NOT NULL,
    name text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    -- One release per tag: two releases on one version would each claim to be
    -- the download for it.
    UNIQUE (repo_id, tag)
);

CREATE INDEX releases_by_repo ON releases(org_id, repo_id, created_at DESC);

-- blob_key is the object's full key in the deployment's bucket, stored rather
-- than recomputed: the object is the thing that must be deleted with the row,
-- and a key rebuilt from a naming convention that later changed would delete
-- nothing while reporting success.
CREATE TABLE release_assets (
    id uuid PRIMARY KEY,
    release_id uuid NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    name text NOT NULL,
    size bigint NOT NULL,
    content_type text NOT NULL DEFAULT 'application/octet-stream',
    blob_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- Assets are addressed by name within a release, both in the API and in a
    -- download URL, so the name has to identify exactly one of them.
    UNIQUE (release_id, name)
);

CREATE INDEX release_assets_by_release ON release_assets(release_id, name);
