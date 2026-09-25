-- Permanent local tombstones fence late delivery after the owner deletes scope.
CREATE TABLE deleted_scopes (org_id uuid NOT NULL, repo_id uuid NOT NULL, PRIMARY KEY (org_id,repo_id));
