-- Git LFS: the pointer is committed to the repository and the payload lives in
-- the deployment's object storage. This table is what makes a payload reachable
-- and, more importantly, what makes it reachable only from the repository it was
-- pushed to: an LFS object is addressed by its content hash, and a content hash
-- is guessable by anyone who has the file, so without a per-repository row a
-- known oid would be downloadable from any repository in the deployment.
--
-- org_id is stored here as well as being reachable through repo_id, for the same
-- reason releases denormalizes it: every query in this package applies an
-- organization predicate taken from the caller's scope, and requiring a join to
-- repositories to apply it is how such a predicate quietly goes missing.
CREATE TABLE lfs_objects (
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    -- The oid is the object's sha256 in lower-case hex, exactly as the LFS batch
    -- API spells it (the "sha256:" prefix belongs to the pointer file, not to
    -- the wire protocol). It is validated against the bytes actually received
    -- before the row is written, so the row is a claim the server checked.
    oid text NOT NULL,
    -- The size that actually arrived, not the size the client declared. A
    -- declared length that did not match the body would otherwise be handed
    -- back in every batch response and every client would report a truncated
    -- download.
    size bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- One row per object per repository: the same file pushed to two
    -- repositories is two rows, because deleting one repository must not take
    -- the other's payload with it. The primary key is also what makes a
    -- re-upload of an object the repository already has a no-op rather than a
    -- duplicate.
    PRIMARY KEY (repo_id, oid)
);

-- The listing and the quota accounting are both per organization and repository.
CREATE INDEX lfs_objects_by_org ON lfs_objects(org_id, repo_id, created_at DESC);
