package work

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/novaforge/novaforge/internal/authz"
)

// SetParent sets id's parent_id to parentID, scoped to the caller's org.
// Materialise (internal/swarm) uses this to attach decomposed subtasks to
// their epic; store.go's Create does not accept parent_id directly, so this
// is the only way to set it.
func (s *Store) SetParent(ctx context.Context, id, parentID uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE work.work_items SET parent_id = $1 WHERE org_id = $2 AND id = $3`,
		parentID, scope.OrgID, id,
	)
	if err != nil {
		return fmt.Errorf("set parent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("work item %s not found", id)
	}
	return nil
}

// SetState transitions id to newState, scoped to the caller's org. Neither
// store.go nor the wider work service currently exposes a general state
// transition, so this is used by the swarm scheduler (Task 3) and by tests
// that need to drive an item to "done" or "blocked" directly.
func (s *Store) SetState(ctx context.Context, id uuid.UUID, newState string) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE work.work_items SET state = $1 WHERE org_id = $2 AND id = $3`,
		newState, scope.OrgID, id,
	)
	if err != nil {
		return fmt.Errorf("set state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("work item %s not found", id)
	}
	return nil
}

// ClaimOpen atomically claims id for execution by transitioning it from
// "open" to "in_progress", returning claimed=false (no error) if it was not
// in state "open" — the compare-and-swap that lets Task 3's scheduler start
// each ready subtask exactly once even under concurrent ticks.
func (s *Store) ClaimOpen(ctx context.Context, id uuid.UUID) (claimed bool, err error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return false, err
	}
	var claimedID uuid.UUID
	err = s.pool.QueryRow(ctx, `
		UPDATE work.work_items SET state = 'in_progress'
		WHERE id = $1 AND org_id = $2 AND state = 'open'
		RETURNING id`,
		id, scope.OrgID,
	).Scan(&claimedID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("claim open: %w", err)
	}
	return true, nil
}

// AddDependency records that blockedID depends on blockerID (blockerID must
// reach state "done" before blockedID is considered Ready). Both items must
// belong to the caller's org. A self-dependency is rejected outright, and a
// dependency that would close a cycle is rejected by a recursive
// reachability check performed BEFORE the insert is attempted.
func (s *Store) AddDependency(ctx context.Context, blockedID, blockerID uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if blockedID == blockerID {
		return fmt.Errorf("work item %s cannot depend on itself", blockedID)
	}

	// Confirm both items exist and belong to the caller's org before doing
	// anything else — no query in this package is satisfiable without the
	// org predicate.
	if _, err := s.get(ctx, scope.OrgID, blockedID); err != nil {
		return fmt.Errorf("add dependency: %w", err)
	}
	if _, err := s.get(ctx, scope.OrgID, blockerID); err != nil {
		return fmt.Errorf("add dependency: %w", err)
	}

	// Cycle check: would blockerID (transitively, following existing
	// blocked->blocker edges) already depend on blockedID? If so, adding
	// blockedID -> blockerID closes a cycle. This recursive reachability
	// check runs BEFORE the insert, so a cyclic edge is never written even
	// transiently.
	var cyclic bool
	err = s.pool.QueryRow(ctx, `
		WITH RECURSIVE ancestors(id) AS (
			SELECT blocker_id FROM work.work_item_deps WHERE blocked_id = $1
			UNION
			SELECT d.blocker_id
			FROM work.work_item_deps d
			JOIN ancestors a ON d.blocked_id = a.id
		)
		SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $2)`,
		blockerID, blockedID,
	).Scan(&cyclic)
	if err != nil {
		return fmt.Errorf("check dependency cycle: %w", err)
	}
	if cyclic {
		return fmt.Errorf("dependency would create a cycle: %s -> %s", blockedID, blockerID)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO work.work_item_deps (blocked_id, blocker_id)
		VALUES ($1, $2)
		ON CONFLICT (blocked_id, blocker_id) DO NOTHING`,
		blockedID, blockerID,
	)
	if err != nil {
		return fmt.Errorf("add dependency: %w", err)
	}
	return nil
}

// Dependencies returns the work items that block id — the items id depends
// on — scoped to the caller's org.
func (s *Store) Dependencies(ctx context.Context, id uuid.UUID) ([]Item, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.org_id, w.repo_id, w.key, w.type, w.goal, w.acceptance,
		       w.constraints, w.required_gates, w.assignee_id, w.assignee_kind,
		       w.state, w.created_at
		FROM work.work_items w
		JOIN work.work_item_deps d ON d.blocker_id = w.id
		WHERE d.blocked_id = $1 AND w.org_id = $2
		ORDER BY w.seq`,
		id, scope.OrgID,
	)
	if err != nil {
		return nil, fmt.Errorf("dependencies: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// Dependents returns the work items that depend on id — the items blocked
// by id — scoped to the caller's org.
func (s *Store) Dependents(ctx context.Context, id uuid.UUID) ([]Item, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.org_id, w.repo_id, w.key, w.type, w.goal, w.acceptance,
		       w.constraints, w.required_gates, w.assignee_id, w.assignee_kind,
		       w.state, w.created_at
		FROM work.work_items w
		JOIN work.work_item_deps d ON d.blocked_id = w.id
		WHERE d.blocker_id = $1 AND w.org_id = $2
		ORDER BY w.seq`,
		id, scope.OrgID,
	)
	if err != nil {
		return nil, fmt.Errorf("dependents: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// Ready returns the children of epicID (work items whose parent_id is
// epicID) that are themselves still open and whose every blocker has
// reached state "done". It is a single query: the "every blocker done"
// condition is expressed as NOT EXISTS an unfinished blocker, so a subtask
// with no blockers at all trivially satisfies it.
func (s *Store) Ready(ctx context.Context, orgID, epicID uuid.UUID) ([]Item, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.org_id, w.repo_id, w.key, w.type, w.goal, w.acceptance,
		       w.constraints, w.required_gates, w.assignee_id, w.assignee_kind,
		       w.state, w.created_at
		FROM work.work_items w
		WHERE w.org_id = $1 AND w.parent_id = $2 AND w.state = 'open'
		  AND NOT EXISTS (
		    SELECT 1 FROM work.work_item_deps d
		    JOIN work.work_items b ON b.id = d.blocker_id
		    WHERE d.blocked_id = w.id AND b.state <> 'done'
		  )
		ORDER BY w.seq`,
		orgID, epicID,
	)
	if err != nil {
		return nil, fmt.Errorf("ready: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// scanItems drains rows of the (id, org_id, repo_id, key, type, goal,
// acceptance, constraints, required_gates, assignee_id, assignee_kind,
// state, created_at) shape shared by Dependencies, Dependents, and Ready.
func scanItems(rows pgx.Rows) ([]Item, error) {
	var items []Item
	for rows.Next() {
		var item Item
		var assigneeID *uuid.UUID
		var assigneeKind *string
		if err := rows.Scan(&item.ID, &item.OrgID, &item.RepoID, &item.Key, &item.Type, &item.Goal,
			&item.Acceptance, &item.Constraints, &item.RequiredGates,
			&assigneeID, &assigneeKind, &item.State, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan work item: %w", err)
		}
		if assigneeID != nil {
			item.AssigneeID = *assigneeID
		}
		if assigneeKind != nil {
			item.AssigneeKind = *assigneeKind
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan work items: %w", err)
	}
	return items, nil
}

// Epic identifies one epic the swarm scheduler should tick, together with
// the organization it belongs to.
type Epic struct {
	OrgID uuid.UUID
	ID    uuid.UUID
}

// OpenEpics returns every Work Item that has at least one child subtask and
// is not yet finished, across all organizations. It is deliberately the one
// query in this package with no organization predicate: the swarm
// scheduler is a platform worker that has no organization of its own, and
// it re-enters each organization's scope explicitly before touching that
// organization's rows. Nothing here returns a Work Item's contents — only
// the pair of ids the scheduler needs to scope itself — so no organization
// boundary is crossed by what is read.
func (s *Store) OpenEpics(ctx context.Context) ([]Epic, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT p.org_id, p.id
		FROM work.work_items p
		JOIN work.work_items c ON c.parent_id = p.id
		WHERE p.state <> 'done'`)
	if err != nil {
		return nil, fmt.Errorf("list open epics: %w", err)
	}
	defer rows.Close()

	var out []Epic
	for rows.Next() {
		var e Epic
		if err := rows.Scan(&e.OrgID, &e.ID); err != nil {
			return nil, fmt.Errorf("scan epic: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// OrganizationsWithWork returns every organization that has at least one
// Work Item. Like OpenEpics it deliberately carries no organization
// predicate: the maintenance sweeper is a platform worker with no
// organization of its own, and it re-enters each organization's scope
// before reading anything belonging to it. Only ids are returned, so
// nothing of one organization's content is visible to another.
func (s *Store) OrganizationsWithWork(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT org_id FROM work.work_items`)
	if err != nil {
		return nil, fmt.Errorf("list organizations with work: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan organization: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Proposal is one maintenance finding that became a Work Item, together with
// the item it created. resolved marks a finding that has stopped reproducing.
type Proposal struct {
	Fingerprint  string
	WorkItemKey  string
	WorkItemGoal string
	WorkItemType string
	State        string
	Resolved     bool
}

// ListProposals returns what the maintenance scanners have proposed for one
// repository, newest first. The join is to work_items because a proposal is
// not a separate kind of thing: it is a plain, unassigned Work Item plus the
// fingerprint that stops the same finding being raised twice.
func (s *Store) ListProposals(ctx context.Context, repoID uuid.UUID) ([]Proposal, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.fingerprint, w.key, w.goal, w.type, w.state, p.resolved_at IS NOT NULL
		FROM work.maintenance_proposals p
		JOIN work.work_items w ON w.id = p.work_item_id
		WHERE p.org_id = $1 AND p.repo_id = $2
		ORDER BY w.created_at DESC`,
		scope.OrgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("list maintenance proposals: %w", err)
	}
	defer rows.Close()

	var out []Proposal
	for rows.Next() {
		var p Proposal
		if err := rows.Scan(&p.Fingerprint, &p.WorkItemKey, &p.WorkItemGoal,
			&p.WorkItemType, &p.State, &p.Resolved); err != nil {
			return nil, fmt.Errorf("scan proposal: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
