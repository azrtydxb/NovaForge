-- Administration of an existing repository. UNIQUE(org_id, name) already exists
-- from 000001, so a rename cannot collide and a transfer into an organization
-- that already has that name is refused by the constraint rather than by a check
-- that could race.
ALTER TABLE repositories ADD COLUMN archived boolean NOT NULL DEFAULT false;
