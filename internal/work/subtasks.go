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
func (s *Store) CreateChild(ctx context.Context, epicID uuid.UUID, key, role string, item Item) (Item, error) {
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
		INSERT INTO work.epic_subtasks (parent_id, key, work_item_id, agent_role) VALUES ($1, $2, $3, $4)`,
		epicID, key, item.ID, role,
	); err != nil {
		return Item{}, fmt.Errorf("create child: record subtask key: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Item{}, fmt.Errorf("create child: commit: %w", err)
	}
	return item, nil
}

// SubtaskRole returns the agent role a materialised subtask was assigned to
// by Planner.Materialise, scoped to the caller's org. It returns an error
// if workItemID was not created through CreateChild (e.g. a top-level Work
// Item, or a child created some other way).
func (s *Store) SubtaskRole(ctx context.Context, workItemID uuid.UUID) (string, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return "", err
	}
	var role string
	err = s.pool.QueryRow(ctx, `
		SELECT es.agent_role
		FROM work.epic_subtasks es
		JOIN work.work_items w ON w.id = es.work_item_id
		WHERE es.work_item_id = $1 AND w.org_id = $2`,
		workItemID, scope.OrgID,
	).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("subtask role: work item %s is not a materialised subtask", workItemID)
		}
		return "", fmt.Errorf("subtask role: %w", err)
	}
	return role, nil
}

// Children returns every work item whose parent_id is epicID, in every
// state, scoped to the caller's org. Unlike Ready, it is not filtered to
// dependency-free open items: the swarm scheduler uses it to see the whole
// picture — how many subtasks are already in_progress (for the
// concurrency cap) and whether any subtask has reached state "blocked"
// (which the scheduler propagates to the epic itself).
func (s *Store) Children(ctx context.Context, orgID, epicID uuid.UUID) ([]Item, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.org_id, w.repo_id, w.key, w.type, w.goal, w.acceptance,
		       w.constraints, w.required_gates, w.assignee_id, w.assignee_kind,
		       w.state, w.created_at, w.execution_claimed
		FROM work.work_items w
		WHERE w.org_id = $1 AND w.parent_id = $2
		ORDER BY w.seq`,
		orgID, epicID,
	)
	if err != nil {
		return nil, fmt.Errorf("children: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}
