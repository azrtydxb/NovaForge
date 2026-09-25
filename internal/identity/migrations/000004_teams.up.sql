-- Teams: a named group inside an organization, carrying a role, so access can be
-- granted to a group rather than to each person in turn. The spec has described
-- these since the beginning and nothing implemented them.
--
-- The name is unique per organization, not globally: two organizations may each
-- have a "reviewers" team and they are two teams.
CREATE TABLE teams (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name text NOT NULL,
    -- The same roles an organization membership carries, because a team grant is
    -- the same kind of thing narrowed to a group.
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

-- org_id is carried on the membership as well as on the team so every query can
-- take the organization predicate directly, rather than reaching it through a
-- join that a mistake could omit.
CREATE TABLE team_members (
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_by_user ON team_members(org_id, user_id);
