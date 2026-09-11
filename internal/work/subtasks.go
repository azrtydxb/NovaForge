package work

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/novaforge/novaforge/internal/authz"
)

// CreateChild creates a child work item of epicID identified by a
// swarm-assigned subtask key, or — if that (epicID, key) pair has already
// been materialised — returns the item created the first time, unchanged.
// Both the insert of the child work item and the recording of the
// (parent_id, key) pair happen in one transaction, so calling CreateChild
// (and therefore swarm.Planner.Materialise, which calls it once per
// subtask) twice with the same epic and key set is idempotent: one child
// per key, never two.
func (s *Store) CreateChild(ctx context.Context, epicID uuid.UUID, key string, item Item) (Item, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Item{}, err
	}
	if !validTypes[item.Type] {
		return Item{}, fmt.Errorf("invalid type %q", item.Type)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Item{}, fmt.Errorf("create child: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var existingID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT work_item_id FROM work.epic_subtasks WHERE parent_id = $1 AND key = $2`,
		epicID, key,
	).Scan(&existingID)
	switch {
	case err == nil:
		// Already materialised for this (epic, key) pair: return the
		// existing child rather than creating a second one.
		return s.get(ctx, scope.OrgID, existingID)
	case errors.Is(err, pgx.ErrNoRows):
		// Not yet materialised; fall through to create it.
	default:
		return Item{}, fmt.Errorf("create child: check existing subtask: %w", err)
	}

	item.ID = uuid.New()
	item.OrgID = scope.OrgID
	if item.Acceptance == nil {
		item.Acceptance = []string{}
	}
	if item.Constraints == nil {
		item.Constraints = []string{}
	}
	if item.RequiredGates == nil {
		item.RequiredGates = []string{}
	}
	if item.State == "" {
		item.State = "open"
	}

	var assigneeID *uuid.UUID
	if item.AssigneeID != uuid.Nil {
		assigneeID = &item.AssigneeID
	}
	var assigneeKind *string
	if item.AssigneeKind != "" {
		assigneeKind = &item.AssigneeKind
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO work.work_items (
			id, org_id, repo_id, parent_id, seq, key, type, goal, acceptance, constraints,
			required_gates, assignee_id, assignee_kind, state
		)
		VALUES (
			$1, $2, $3, $4,
			COALESCE((SELECT MAX(seq) FROM work.work_items WHERE org_id = $2), 0) + 1,
			'NF-' || (COALESCE((SELECT MAX(seq) FROM work.work_items WHERE org_id = $2), 0) + 1),
			$5, $6, $7, $8, $9, $10, $11, $12
		)
		RETURNING key, created_at`,
		item.ID, item.OrgID, item.RepoID, epicID,
		item.Type, item.Goal, item.Acceptance, item.Constraints, item.RequiredGates,
		assigneeID, assigneeKind, item.State,
	).Scan(&item.Key, &item.CreatedAt)
	if err != nil {
		return Item{}, fmt.Errorf("create child: insert work item: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO work.epic_subtasks (parent_id, key, work_item_id) VALUES ($1, $2, $3)`,
		epicID, key, item.ID,
	); err != nil {
		return Item{}, fmt.Errorf("create child: record subtask key: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Item{}, fmt.Errorf("create child: commit: %w", err)
	}
	return item, nil
}
