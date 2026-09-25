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
	if repoID == uuid.Nil {
		return fmt.Errorf("purge requires repository ID")
	}
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	repo := uuid.Nil
	if repoID == nil {
		_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, orgLockKey(orgID))
	} else {
		repo = *repoID
		_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, orgLockKey(orgID))
		if err == nil {
			_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, repoLockKey(orgID, repo))
		}
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO graph.index_tombstones(org_id,repo_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, orgID, repo); err != nil {
		return err
	}
	// References have no cascading foreign key. Purge all source evidence and
	// its checkpoint atomically with the permanent write fence.
	for _, table := range []string{"semantic_snapshots", "file_references", "code_chunks", "graph_nodes"} {
		predicate := "org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)"
		if table == "graph_nodes" {
			// Legacy checkpoint nodes predate repo_id ownership.
			predicate = "org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2 OR (kind = 'commit' AND key = $2::text))"
		}
		if _, err := tx.Exec(ctx, "DELETE FROM graph."+table+" WHERE "+predicate, orgID, repoID); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	return tx.Commit(ctx)
}
