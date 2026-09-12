// Package work owns the work service's schema: typed engineering intent
// (Work Items) that can be assigned to a human or an agent. No other service
// reads these tables directly.
package work

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
)

// Item is a row in the work.work_items table.
type Item struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	RepoID        uuid.UUID
	Key           string
	Type          string
	Goal          string
	Acceptance    []string
	Constraints   []string
	RequiredGates []string
	AssigneeID    uuid.UUID
	AssigneeKind  string
	State         string
	CreatedAt     time.Time
}

var validTypes = map[string]bool{
	"feature":       true,
	"bug":           true,
	"refactor":      true,
	"security":      true,
	"tech_debt":     true,
	"research":      true,
	"architecture":  true,
	"upgrade":       true,
	"incident":      true,
	"documentation": true,
}

const checkViolation = "23514"

// Store provides access to the work schema's tables.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as a work.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Create inserts a new work item, allocating its org-scoped sequence and key
// inside the insert statement so concurrent creates cannot collide.
func (s *Store) Create(ctx context.Context, item Item) (Item, error) {
	if !validTypes[item.Type] {
		return Item{}, fmt.Errorf("invalid type %q", item.Type)
	}

	item.ID = uuid.New()
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

	err := s.pool.QueryRow(ctx, `
		INSERT INTO work.work_items (
			id, org_id, repo_id, seq, key, type, goal, acceptance, constraints,
			required_gates, assignee_id, assignee_kind, state
		)
		VALUES (
			$1, $2, $3,
			COALESCE((SELECT MAX(seq) FROM work.work_items WHERE org_id = $2), 0) + 1,
			'NF-' || (COALESCE((SELECT MAX(seq) FROM work.work_items WHERE org_id = $2), 0) + 1),
			$4, $5, $6, $7, $8, $9, $10, $11
		)
		RETURNING seq, key, created_at`,
		item.ID, item.OrgID, item.RepoID,
		item.Type, item.Goal, item.Acceptance, item.Constraints, item.RequiredGates,
		assigneeID, assigneeKind, item.State,
	).Scan(new(int64), &item.Key, &item.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == checkViolation {
			return Item{}, fmt.Errorf("invalid type %q", item.Type)
		}
		return Item{}, fmt.Errorf("create work item: %w", err)
	}
	return item, nil
}

// Get looks up a work item by id, scoped to the org carried in ctx.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Item, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Item{}, err
	}
	return s.get(ctx, scope.OrgID, id)
}

func (s *Store) get(ctx context.Context, orgID, id uuid.UUID) (Item, error) {
	var item Item
	var assigneeID *uuid.UUID
	var assigneeKind *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, repo_id, key, type, goal, acceptance, constraints,
		       required_gates, assignee_id, assignee_kind, state, created_at
		FROM work.work_items WHERE org_id = $1 AND id = $2`,
		orgID, id,
	).Scan(&item.ID, &item.OrgID, &item.RepoID, &item.Key, &item.Type, &item.Goal,
		&item.Acceptance, &item.Constraints, &item.RequiredGates,
		&assigneeID, &assigneeKind, &item.State, &item.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Item{}, fmt.Errorf("work item %s not found: %w", id, err)
		}
		return Item{}, fmt.Errorf("get work item: %w", err)
	}
	if assigneeID != nil {
		item.AssigneeID = *assigneeID
	}
	if assigneeKind != nil {
		item.AssigneeKind = *assigneeKind
	}
	return item, nil
}

// GetByKey looks up a work item by its human-readable key within orgID.
func (s *Store) GetByKey(ctx context.Context, orgID uuid.UUID, key string) (Item, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return Item{}, err
	}
	var item Item
	var assigneeID *uuid.UUID
	var assigneeKind *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, repo_id, key, type, goal, acceptance, constraints,
		       required_gates, assignee_id, assignee_kind, state, created_at
		FROM work.work_items WHERE org_id = $1 AND key = $2`,
		orgID, key,
	).Scan(&item.ID, &item.OrgID, &item.RepoID, &item.Key, &item.Type, &item.Goal,
		&item.Acceptance, &item.Constraints, &item.RequiredGates,
		&assigneeID, &assigneeKind, &item.State, &item.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Item{}, fmt.Errorf("work item %q not found: %w", key, err)
		}
		return Item{}, fmt.Errorf("get work item by key: %w", err)
	}
	if assigneeID != nil {
		item.AssigneeID = *assigneeID
	}
	if assigneeKind != nil {
		item.AssigneeKind = *assigneeKind
	}
	return item, nil
}

// List returns work items for orgID and repoID, optionally filtered by state.
func (s *Store) List(ctx context.Context, orgID, repoID uuid.UUID, state string) ([]Item, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, repo_id, key, type, goal, acceptance, constraints,
		       required_gates, assignee_id, assignee_kind, state, created_at
		FROM work.work_items
		WHERE org_id = $1 AND repo_id = $2 AND ($3 = '' OR state = $3)
		ORDER BY seq`,
		orgID, repoID, state,
	)
	if err != nil {
		return nil, fmt.Errorf("list work items: %w", err)
	}
	defer rows.Close()

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
		return nil, fmt.Errorf("list work items: %w", err)
	}
	return items, nil
}

// Assign sets the assignee of a work item, scoped to the org carried in ctx.
func (s *Store) Assign(ctx context.Context, id, assigneeID uuid.UUID, kind string) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if kind != "user" && kind != "agent" {
		return fmt.Errorf("invalid assignee kind %q", kind)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE work.work_items SET assignee_id = $1, assignee_kind = $2
		WHERE org_id = $3 AND id = $4`,
		assigneeID, kind, scope.OrgID, id,
	)
	if err != nil {
		return fmt.Errorf("assign work item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("work item %s not found", id)
	}
	return nil
}

// Comment is one entry in a Work Item's discussion thread.
type Comment struct {
	ID         uuid.UUID
	WorkItemID uuid.UUID
	AuthorID   uuid.UUID
	AuthorKind string
	Body       string
	CreatedAt  time.Time
}

// AddComment appends body to workItemID's thread, attributed to the caller
// in scope. The Work Item is re-read under the caller's organization first,
// so commenting cannot be used to discover that an item exists in another
// organization.
func (s *Store) AddComment(ctx context.Context, workItemID uuid.UUID, body string) (Comment, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Comment{}, err
	}
	if body == "" {
		return Comment{}, fmt.Errorf("a comment needs a body")
	}
	if _, err := s.get(ctx, scope.OrgID, workItemID); err != nil {
		return Comment{}, err
	}

	kind := scope.ActorKind
	if kind != "agent" {
		kind = "user"
	}
	c := Comment{WorkItemID: workItemID, AuthorID: scope.ActorID, AuthorKind: kind, Body: body}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO work.work_item_comments (org_id, work_item_id, author_id, author_kind, body)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		scope.OrgID, workItemID, scope.ActorID, kind, body,
	).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return Comment{}, fmt.Errorf("add comment: %w", err)
	}
	return c, nil
}

// ListComments returns workItemID's thread oldest first.
func (s *Store) ListComments(ctx context.Context, workItemID uuid.UUID) ([]Comment, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, work_item_id, author_id, author_kind, body, created_at
		FROM work.work_item_comments
		WHERE org_id = $1 AND work_item_id = $2
		ORDER BY created_at`,
		scope.OrgID, workItemID)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()

	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.WorkItemID, &c.AuthorID, &c.AuthorKind, &c.Body, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SortedTypes returns every legal Work Item type in sorted order. It is
// exported so a caller that must name the legal set — a prompt telling a
// model what it may choose, a validation error explaining what it may not —
// reads it from the one place that decides, rather than keeping a second
// copy that drifts.
func SortedTypes() []string {
	out := make([]string, 0, len(validTypes))
	for t := range validTypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// IsValidType reports whether t is a legal Work Item type.
func IsValidType(t string) bool { return validTypes[t] }
