package approvals

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// ApprovalRequest is a row in the approvals.approval_requests table: one
// request for a human decision on an Action, and its outcome once decided.
type ApprovalRequest struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	RunID     uuid.UUID
	Action    Action
	Detail    map[string]any
	Decision  string // "pending", "approved", or "denied"
	DecidedBy uuid.UUID
	DecidedAt time.Time
	CreatedAt time.Time
}

// Store provides access to the approvals schema's approval_requests table.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as an approvals.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Request records a new pending approval request.
func (s *Store) Request(ctx context.Context, req ApprovalRequest) (ApprovalRequest, error) {
	if err := authz.RequireOrg(ctx, req.OrgID); err != nil {
		return ApprovalRequest{}, err
	}
	if req.ID == uuid.Nil {
		req.ID = uuid.New()
	}
	if req.Decision == "" {
		req.Decision = "pending"
	}
	if req.Detail == nil {
		req.Detail = map[string]any{}
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO approvals.approval_requests (id, org_id, run_id, action, detail, decision)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		req.ID, req.OrgID, req.RunID, string(req.Action), req.Detail, req.Decision,
	).Scan(&req.CreatedAt)
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("request approval: %w", err)
	}
	return req, nil
}

// Resolve records decidedBy's decision ("approved" or "denied") on id.
func (s *Store) Resolve(ctx context.Context, id, decidedBy uuid.UUID, decision string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE approvals.approval_requests
		SET decision = $1, decided_by = $2, decided_at = now()
		WHERE id = $3`,
		decision, decidedBy, id,
	)
	if err != nil {
		return fmt.Errorf("resolve approval %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("approval request %s not found", id)
	}
	return nil
}

// Pending returns every approval request still awaiting a decision, scoped
// to the caller's org.
func (s *Store) Pending(ctx context.Context) ([]ApprovalRequest, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, run_id, action, detail, decision, created_at
		FROM approvals.approval_requests
		WHERE org_id = $1 AND decision = 'pending'
		ORDER BY created_at`,
		scope.OrgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()

	var out []ApprovalRequest
	for rows.Next() {
		var req ApprovalRequest
		var action string
		if err := rows.Scan(&req.ID, &req.OrgID, &req.RunID, &action, &req.Detail, &req.Decision, &req.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan approval request: %w", err)
		}
		req.Action = Action(action)
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	return out, nil
}
