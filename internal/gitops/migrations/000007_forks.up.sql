-- A fork is a full repository, so it is an ordinary row in this table with one
-- extra fact recorded: which repository its history came from. Nothing about
-- reading or writing a fork consults the parent, which is what keeps a fork
-- working after its parent is archived, renamed or deleted.
--
-- ON DELETE SET NULL rather than CASCADE: deleting the upstream must not delete
-- everyone's forks of it. The fork then simply stops claiming a parent, which
-- is the truth once the parent is gone.
ALTER TABLE repositories ADD COLUMN parent_repo_id uuid REFERENCES repositories(id) ON DELETE SET NULL;

-- Listing "the forks of this repository" is the one query this column exists
-- for, and it is always made within one organization's scope.
CREATE INDEX repositories_by_parent ON repositories(org_id, parent_repo_id);
