package graph

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

// PurgeRepository deletes a deleted repository's graph nodes (their edges
// cascade) and indexed code chunks in the caller's organization. A deleted
// repository's code otherwise went on answering searches.
func (s *Store) PurgeRepository(ctx context.Context, repoID uuid.UUID) error {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return err
	}
	return s.purge(ctx, orgID, &repoID)
}

// PurgeOrganization deletes every graph node and code chunk of the caller's
// organization.
func (s *Store) PurgeOrganization(ctx context.Context) error {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return err
	}
	return s.purge(ctx, orgID, nil)
}

func (s *Store) purge(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) error {
	// References are retained by name so re-indexing either endpoint can
	// rebuild edges. They have no node foreign key, so deleting nodes does
	// not cascade to them. Leaving them behind retains deleted source data.
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM graph.file_references WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`, orgID, repoID); err != nil {
		return fmt.Errorf("purge file references: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM graph.code_chunks WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`, orgID, repoID); err != nil {
		return fmt.Errorf("purge code chunks: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM graph.graph_nodes WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`, orgID, repoID); err != nil {
		return fmt.Errorf("purge graph nodes: %w", err)
	}
	return nil
}
