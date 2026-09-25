-- Immutable UUID tombstones fence delayed index writes after deletion. The nil
-- repository UUID denotes the entire organization, including unseen repositories.
CREATE TABLE index_tombstones (
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    PRIMARY KEY (org_id, repo_id)
);
