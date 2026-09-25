package gitops

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/metadata"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// Collaborator is access to one repository, held by a person or by a team.
//
// It is a second path to a repository, never a wider one: organization membership
// is checked first, and this is consulted only when that fails. A member's access
// is unchanged by anything here.
type Collaborator struct {
	RepoID uuid.UUID
	UserID uuid.UUID // zero when the grant names a team
	TeamID uuid.UUID // zero when the grant names a person
	Role   string    // "read" or "write"
}

// TeamLookup reports which teams a person belongs to in an organization.
//
// Teams live in Identity's schema, which git-platform may not read, so a grant
// naming a team is resolved by asking Identity. The interface exists so the
// collaborator store can be tested without standing up an Identity server.
type TeamLookup func(ctx context.Context, orgID, userID uuid.UUID) ([]uuid.UUID, error)

// NewIdentityTeamLookup resolves a person's teams through Identity's RPC.
//
// It presents git-platform's own service credential rather than forwarding the
// caller's: this runs while the caller is still being authenticated, and an outside
// collaborator has no standing in the organization to ask Identity anything. The
// organization comes from the repository being reached, not from the credential.
func NewIdentityTeamLookup(client identityv1.IdentityServiceClient, hmacSecret string) TeamLookup {
	return func(ctx context.Context, orgID, userID uuid.UUID) ([]uuid.UUID, error) {
		token, err := svcauth.Mint(hmacSecret, "git-platform", orgID, svcauth.DefaultTTL)
		if err != nil {
			return nil, fmt.Errorf("mint team lookup credential: %w", err)
		}
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
		resp, err := client.ListTeamsForUser(ctx, &identityv1.ListTeamsForUserRequest{UserId: userID.String()})
		if err != nil {
			return nil, fmt.Errorf("list teams for user: %w", err)
		}
		out := make([]uuid.UUID, 0, len(resp.GetTeams()))
		for _, t := range resp.GetTeams() {
			id, err := uuid.Parse(t.GetId())
			if err != nil {
				continue
			}
			out = append(out, id)
		}
		return out, nil
	}
}

// CollaboratorStore reads and writes repository grants. It is separate from Server
// because the git transports need it without the whole RPC surface.
type CollaboratorStore struct {
	pool  *pgxpool.Pool
	teams TeamLookup
}

// NewCollaboratorStore builds the store. teams may be nil, in which case a grant
// naming a team reaches nobody — which is the safe reading when the lookup is
// unavailable, rather than admitting everyone the grant might have covered.
func NewCollaboratorStore(pool *pgxpool.Pool, teams TeamLookup) *CollaboratorStore {
	return &CollaboratorStore{pool: pool, teams: teams}
}

// AddCollaborator grants access to a repository. Exactly one of userID or teamID is
// set; the CHECK in the schema refuses both or neither, and this refuses it earlier
// with a clearer message.
func (c *CollaboratorStore) AddCollaborator(ctx context.Context, orgID, repoID, userID, teamID uuid.UUID, role string) error {
	if (userID == uuid.Nil) == (teamID == uuid.Nil) {
		return errors.New("a grant names exactly one of a person or a team")
	}
	if role != "read" && role != "write" {
		return fmt.Errorf("role %q is not read or write", role)
	}
	var user, team *uuid.UUID
	if userID != uuid.Nil {
		user = &userID
	}
	if teamID != uuid.Nil {
		team = &teamID
	}
	// The repository is matched on its organization too, so a caller cannot grant
	// access to another organization's repository by naming its id.
	tag, err := c.pool.Exec(ctx, `
		INSERT INTO gitplatform.repository_collaborators (org_id, repo_id, user_id, team_id, role)
		SELECT $1, id, $3, $4, $5 FROM gitplatform.repositories WHERE id = $2 AND org_id = $1
		ON CONFLICT DO NOTHING`, orgID, repoID, user, team, role)
	if err != nil {
		return fmt.Errorf("add collaborator: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the repository is not this organization's, or the grant already
		// exists. A repeated grant is not a failure; a missing repository is.
		var exists bool
		if err := c.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM gitplatform.repositories WHERE id = $1 AND org_id = $2)`,
			repoID, orgID).Scan(&exists); err != nil {
			return fmt.Errorf("add collaborator: %w", err)
		}
		if !exists {
			return fmt.Errorf("no repository %s in this organization", repoID)
		}
	}
	return nil
}

// RemoveCollaborator revokes a grant.
func (c *CollaboratorStore) RemoveCollaborator(ctx context.Context, orgID, repoID, userID, teamID uuid.UUID) error {
	var user, team *uuid.UUID
	if userID != uuid.Nil {
		user = &userID
	}
	if teamID != uuid.Nil {
		team = &teamID
	}
	tag, err := c.pool.Exec(ctx, `
		DELETE FROM gitplatform.repository_collaborators
		WHERE org_id = $1 AND repo_id = $2
		  AND user_id IS NOT DISTINCT FROM $3 AND team_id IS NOT DISTINCT FROM $4`,
		orgID, repoID, user, team)
	if err != nil {
		return fmt.Errorf("remove collaborator: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("no such grant on this repository")
	}
	return nil
}

// ListCollaborators returns a repository's grants.
func (c *CollaboratorStore) ListCollaborators(ctx context.Context, orgID, repoID uuid.UUID) ([]Collaborator, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT repo_id, coalesce(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       coalesce(team_id, '00000000-0000-0000-0000-000000000000'::uuid), role
		FROM gitplatform.repository_collaborators
		WHERE org_id = $1 AND repo_id = $2
		ORDER BY created_at`, orgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("list collaborators: %w", err)
	}
	defer rows.Close()
	out := []Collaborator{}
	for rows.Next() {
		var g Collaborator
		if err := rows.Scan(&g.RepoID, &g.UserID, &g.TeamID, &g.Role); err != nil {
			return nil, fmt.Errorf("scan collaborator: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReposForUser returns the repositories a person holds in an organization by grant,
// whether granted to them directly or to a team they belong to.
//
// This is what a repository-limited scope is built from, so it decides what an
// outside collaborator can reach. A team lookup that fails is an error rather than
// an empty set: reporting "no repositories" when the answer is unknown would look
// like a revoked grant and hide an outage.
func (c *CollaboratorStore) ReposForUser(ctx context.Context, orgID, userID uuid.UUID) (map[uuid.UUID]string, error) {
	access := map[uuid.UUID]string{}
	rows, err := c.pool.Query(ctx, `
		SELECT repo_id, role FROM gitplatform.repository_collaborators
		WHERE org_id = $1 AND user_id = $2`, orgID, userID)
	if err != nil {
		return nil, fmt.Errorf("repositories for user: %w", err)
	}
	for rows.Next() {
		var id uuid.UUID
		var role string
		if err := rows.Scan(&id, &role); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		access[id] = role
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if c.teams == nil {
		return access, nil
	}
	teams, err := c.teams(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	if len(teams) == 0 {
		return access, nil
	}
	teamRows, err := c.pool.Query(ctx, `
		SELECT repo_id, role FROM gitplatform.repository_collaborators
		WHERE org_id = $1 AND team_id = ANY($2)`, orgID, teams)
	if err != nil {
		return nil, fmt.Errorf("repositories for teams: %w", err)
	}
	defer teamRows.Close()
	for teamRows.Next() {
		var id uuid.UUID
		var role string
		if err := teamRows.Scan(&id, &role); err != nil {
			return nil, fmt.Errorf("scan team grant: %w", err)
		}
		// The stronger of the two wins: holding write directly and read through a
		// team is write, and a team granting write is not narrowed by a direct
		// read grant.
		if existing, ok := access[id]; !ok || (existing == "read" && role == "write") {
			access[id] = role
		}
	}
	return access, teamRows.Err()
}

// MayAccess reports the role a person holds on one repository by grant, and whether
// they hold one at all.
func (c *CollaboratorStore) MayAccess(ctx context.Context, orgID, repoID, userID uuid.UUID) (string, bool, error) {
	access, err := c.ReposForUser(ctx, orgID, userID)
	if err != nil {
		return "", false, err
	}
	role, ok := access[repoID]
	return role, ok, nil
}

var _ = pgx.ErrNoRows
