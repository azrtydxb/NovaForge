package knowledge

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
)

// PurgeRepository deletes a deleted repository's knowledge entries in the
// caller's organization; with repoID nil, every entry of the organization.
func (s *Store) PurgeRepository(ctx context.Context, repoID *uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return errors.New("purge requires an organization scope")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("purge knowledge: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	// superseded_by does not cascade; every link into the doomed set is cut
	// first so the delete is not refused part-way.
	if _, err := tx.Exec(ctx, `
		UPDATE knowledge.knowledge_entries SET superseded_by = NULL
		WHERE org_id = $1 AND superseded_by IN (
			SELECT id FROM knowledge.knowledge_entries WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2))`,
		scope.OrgID, repoID); err != nil {
		return fmt.Errorf("purge knowledge: unlink: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM knowledge.knowledge_entries WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`,
		scope.OrgID, repoID); err != nil {
		return fmt.Errorf("purge knowledge: %w", err)
	}
	return tx.Commit(ctx)
}
