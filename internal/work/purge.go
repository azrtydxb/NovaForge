package work

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
)

// errNoPurgeScope refuses a purge with no organization in scope: a delete
// with no org predicate would reach every organization.
var errNoPurgeScope = errors.New("purge requires an organization scope")

func purgeOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return uuid.Nil, errNoPurgeScope
	}
	return scope.OrgID, nil
}

// PurgeRepository deletes every Work Item of a deleted repository, with the
// comments, dependencies, subtask links and maintenance proposals that hang
// off them, within the caller's organization. It returns how many Work Items
// it removed and is safe to repeat.
//
// A deleted repository used to leave all of these behind: its name no longer
// resolved, so nothing could reach them, and nothing ever removed them.
func (s *Store) PurgeRepository(ctx context.Context, repoID uuid.UUID) (int64, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return 0, err
	}
	return s.purge(ctx, orgID, &repoID)
}

// PurgeOrganization deletes every Work Item of the caller's organization.
func (s *Store) PurgeOrganization(ctx context.Context) (int64, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return 0, err
	}
	return s.purge(ctx, orgID, nil)
}

func (s *Store) purge(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge work items: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	// parent_id does not cascade: an epic's subtasks point at it. Every link
	// into the doomed set is cut first, including from an item elsewhere.
	if _, err := tx.Exec(ctx, `
		UPDATE work.work_items SET parent_id = NULL
		WHERE org_id = $1 AND parent_id IN (
			SELECT id FROM work.work_items WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2))`,
		orgID, repoID); err != nil {
		return 0, fmt.Errorf("purge work items: unlink parents: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM work.maintenance_proposals WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`,
		orgID, repoID); err != nil {
		return 0, fmt.Errorf("purge maintenance proposals: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM work.work_items WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`,
		orgID, repoID)
	if err != nil {
		return 0, fmt.Errorf("purge work items: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("purge work items: %w", err)
	}
	return tag.RowsAffected(), nil
}
