-- Access to one repository, granted to a person or to a team, without requiring
-- membership of the organization that owns it.
--
-- A grant names exactly one of a person or a team: both would be ambiguous about
-- whose access it is, and neither grants nothing. The CHECK makes that structural
-- rather than a rule a writer has to remember.
CREATE TABLE repository_collaborators (
    org_id uuid NOT NULL,
    repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    user_id uuid,
    team_id uuid,
    role text NOT NULL CHECK (role IN ('read', 'write')),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((user_id IS NULL) <> (team_id IS NULL))
);

-- One grant per subject per repository, enforced separately for people and teams
-- because a partial unique index cannot span a nullable pair.
CREATE UNIQUE INDEX repository_collaborators_user
    ON repository_collaborators(repo_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX repository_collaborators_team
    ON repository_collaborators(repo_id, team_id) WHERE team_id IS NOT NULL;

-- Resolving access at a push looks up every repository a person holds in an
-- organization, so that is the shape the index serves.
CREATE INDEX repository_collaborators_by_user ON repository_collaborators(org_id, user_id)
    WHERE user_id IS NOT NULL;
