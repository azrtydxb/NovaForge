package identity

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Team is a named group inside an organization, carrying a role.
//
// The role is the team's own, not the strongest role of its members: a team that
// grants "member" grants that much to an owner too. Reading the organization role
// here would make every team as powerful as its most privileged member, which is
// the opposite of what a narrower grant is for.
type Team struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	Name  string
	Role  string
}

// CreateTeam adds a team to an organization. The name is unique per organization,
// so two organizations may each have a team of the same name.
func (s *Store) CreateTeam(ctx context.Context, orgID uuid.UUID, name, role string) (Team, error) {
	if name == "" {
		return Team{}, fmt.Errorf("team name is required")
	}
	t := Team{OrgID: orgID, Name: name, Role: role}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO identity.teams (org_id, name, role) VALUES ($1, $2, $3) RETURNING id`,
		orgID, name, role).Scan(&t.ID)
	if err != nil {
		return Team{}, fmt.Errorf("create team: %w", err)
	}
	return t, nil
}

// ListTeams returns an organization's teams.
func (s *Store) ListTeams(ctx context.Context, orgID uuid.UUID) ([]Team, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, org_id, name, role FROM identity.teams WHERE org_id = $1 ORDER BY name`,
		orgID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.Role); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTeam removes a team and, by cascade, its memberships. The grants that
// named the team are removed with it by their own cascade, so no grant is left
// pointing at a team that no longer exists.
func (s *Store) DeleteTeam(ctx context.Context, orgID, teamID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM identity.teams WHERE id = $1 AND org_id = $2`, teamID, orgID)
	if err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no team %s in this organization", teamID)
	}
	return nil
}

// AddTeamMember puts a person in a team. Adding someone already in it succeeds and
// changes nothing: a caller retrying after a lost response must not be told the
// person is already there as though that were a failure.
func (s *Store) AddTeamMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error {
	// The team is matched on its organization as well as its id, so a caller
	// cannot add a person to another organization's team by naming its id.
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO identity.team_members (org_id, team_id, user_id)
		 SELECT $1, id, $3 FROM identity.teams WHERE id = $2 AND org_id = $1
		 ON CONFLICT (team_id, user_id) DO NOTHING`,
		orgID, teamID, userID)
	if err != nil {
		return fmt.Errorf("add team member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the team is not this organization's, or the person was already a
		// member. Tell them apart rather than reporting a success that did nothing.
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM identity.team_members WHERE team_id = $1 AND user_id = $2 AND org_id = $3)`,
			teamID, userID, orgID).Scan(&exists); err != nil {
			return fmt.Errorf("add team member: %w", err)
		}
		if !exists {
			return fmt.Errorf("no team %s in this organization", teamID)
		}
	}
	return nil
}

// RemoveTeamMember takes a person out of a team.
func (s *Store) RemoveTeamMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM identity.team_members WHERE org_id = $1 AND team_id = $2 AND user_id = $3`,
		orgID, teamID, userID)
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s is not in team %s", userID, teamID)
	}
	return nil
}

// ListTeamMembers returns the user ids in a team.
func (s *Store) ListTeamMembers(ctx context.Context, orgID, teamID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id FROM identity.team_members WHERE org_id = $1 AND team_id = $2 ORDER BY added_at`,
		orgID, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan team member: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// TeamsForUser returns the teams a person belongs to in an organization. This is
// what a repository grant naming a team resolves through, so it is the one query
// that decides whether a team grant reaches a person.
func (s *Store) TeamsForUser(ctx context.Context, orgID, userID uuid.UUID) ([]Team, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.id, t.org_id, t.name, t.role
		 FROM identity.teams t
		 JOIN identity.team_members m ON m.team_id = t.id AND m.org_id = t.org_id
		 WHERE t.org_id = $1 AND m.user_id = $2
		 ORDER BY t.name`,
		orgID, userID)
	if err != nil {
		return nil, fmt.Errorf("teams for user: %w", err)
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.Role); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
