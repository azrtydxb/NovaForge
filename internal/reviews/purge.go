package reviews

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
)

func purgeOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return uuid.Nil, errors.New("purge requires an organization scope")
	}
	return scope.OrgID, nil
}

// RunIDsForRepository returns the ids of a repository's Engineering Runs in
// the caller's organization. A purge names them to the services that keep
// rows keyed on runs (gate evaluations) before the runs are deleted: once the
// rows are gone, nothing could say which ids were theirs.
func (s *Store) RunIDsForRepository(ctx context.Context, repoID uuid.UUID) ([]uuid.UUID, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return nil, err
	}
	return s.runIDs(ctx, orgID, &repoID)
}

// RunIDsForOrganization returns the ids of every Engineering Run in the
// caller's organization.
func (s *Store) RunIDsForOrganization(ctx context.Context) ([]uuid.UUID, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return nil, err
	}
	return s.runIDs(ctx, orgID, nil)
}

func (s *Store) runIDs(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM reviews.runs WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`, orgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("list runs to purge: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PurgeRepository deletes a deleted repository's Engineering Runs — with their
// plans, proof, comments and reviews — in the caller's organization.
func (s *Store) PurgeRepository(ctx context.Context, repoID uuid.UUID) (int64, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return 0, err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM reviews.runs WHERE org_id = $1 AND repo_id = $2`, orgID, repoID)
	if err != nil {
		return 0, fmt.Errorf("purge runs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PurgeOrganization deletes every Engineering Run of the caller's organization.
func (s *Store) PurgeOrganization(ctx context.Context) (int64, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return 0, err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM reviews.runs WHERE org_id = $1`, orgID)
	if err != nil {
		return 0, fmt.Errorf("purge runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
