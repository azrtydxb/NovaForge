package approvals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// Decision states an approval request moves through. A request is pending
// until a person decides it, or superseded when the change it described moved
// on to a new head before anyone did.
const (
	StatePending    = "pending"
	StateApproved   = "approved"
	StateDenied     = "denied"
	StateSuperseded = "superseded"
)

// ErrNotPending is returned when a decision is recorded on a request that is
// not pending in the caller's organization — already decided, superseded, or
// another organization's.
var ErrNotPending = errors.New("no pending approval request with that id in this organization")

// ApprovalRequest is a row in the approvals.approval_requests table: one
// request for a human decision on an Action, and its outcome once decided.
type ApprovalRequest struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	RunID     uuid.UUID
	Action    Action
	Detail    map[string]any
	Decision  string // "pending", "approved", "denied", or "superseded"
	DecidedBy uuid.UUID
	DecidedAt time.Time
	CreatedAt time.Time

	// HeadSHA is the commit of the change the request is about; empty for a
	// request raised by hand rather than by the gate controller, which
	// therefore never satisfies a merge.
	HeadSHA string
	// Reason and Paths say why the platform asked, derived from the diff.
	Reason string
	Paths  []string
	// Comment is what the person who decided said about it.
	Comment    string
	AuthorID   uuid.UUID
	AuthorKind string
}

// Store provides access to the approvals schema's approval_requests table.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as an approvals.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const requestColumns = `id, org_id, run_id, action, detail, decision, decided_by, decided_at, created_at,
	head_sha, reason, paths, comment, author_id, author_kind`

func scanRequest(row pgx.Row) (ApprovalRequest, error) {
	var (
		req       ApprovalRequest
		action    string
		decidedBy *uuid.UUID
		decidedAt *time.Time
		authorID  *uuid.UUID
	)
	if err := row.Scan(&req.ID, &req.OrgID, &req.RunID, &action, &req.Detail, &req.Decision,
		&decidedBy, &decidedAt, &req.CreatedAt,
		&req.HeadSHA, &req.Reason, &req.Paths, &req.Comment, &authorID, &req.AuthorKind); err != nil {
		return ApprovalRequest{}, err
	}
	req.Action = Action(action)
	if decidedBy != nil {
		req.DecidedBy = *decidedBy
	}
	if decidedAt != nil {
		req.DecidedAt = *decidedAt
	}
	if authorID != nil {
		req.AuthorID = *authorID
	}
	return req, nil
}

func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
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
		req.Decision = StatePending
	}
	if req.Detail == nil {
		req.Detail = map[string]any{}
	}
	if req.Paths == nil {
		req.Paths = []string{}
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO approvals.approval_requests
		  (id, org_id, run_id, action, detail, decision, head_sha, reason, paths, author_id, author_kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING created_at`,
		req.ID, req.OrgID, req.RunID, string(req.Action), req.Detail, req.Decision,
		req.HeadSHA, req.Reason, req.Paths, nullableUUID(req.AuthorID), req.AuthorKind,
	).Scan(&req.CreatedAt)
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("request approval: %w", err)
	}
	return req, nil
}

// Raise returns the approval request for (req.RunID, req.Action, req.HeadSHA),
// creating it pending when none exists, and marks any request still pending
// for an earlier head of the same run and action superseded. It is what the
// gate controller calls every time it is asked about a run, so it converges:
// asking twice, or concurrently, yields the same row.
func (s *Store) Raise(ctx context.Context, req ApprovalRequest) (ApprovalRequest, error) {
	if err := authz.RequireOrg(ctx, req.OrgID); err != nil {
		return ApprovalRequest{}, err
	}
	if req.HeadSHA == "" {
		return ApprovalRequest{}, errors.New("raise approval: a head sha is required")
	}
	if req.Detail == nil {
		req.Detail = map[string]any{}
	}
	if req.Paths == nil {
		req.Paths = []string{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("raise approval: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO approvals.approval_requests
		  (id, org_id, run_id, action, detail, decision, head_sha, reason, paths, author_id, author_kind)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8, $9, $10)
		ON CONFLICT (run_id, action, head_sha) WHERE head_sha <> '' DO NOTHING`,
		uuid.New(), req.OrgID, req.RunID, string(req.Action), req.Detail,
		req.HeadSHA, req.Reason, req.Paths, nullableUUID(req.AuthorID), req.AuthorKind,
	); err != nil {
		return ApprovalRequest{}, fmt.Errorf("raise approval: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE approvals.approval_requests SET decision = 'superseded'
		WHERE org_id = $1 AND run_id = $2 AND action = $3 AND head_sha <> $4 AND decision = 'pending'`,
		req.OrgID, req.RunID, string(req.Action), req.HeadSHA,
	); err != nil {
		return ApprovalRequest{}, fmt.Errorf("supersede earlier approvals: %w", err)
	}
	got, err := scanRequest(tx.QueryRow(ctx, `
		SELECT `+requestColumns+` FROM approvals.approval_requests
		WHERE org_id = $1 AND run_id = $2 AND action = $3 AND head_sha = $4`,
		req.OrgID, req.RunID, string(req.Action), req.HeadSHA))
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("read raised approval: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ApprovalRequest{}, fmt.Errorf("raise approval: %w", err)
	}
	return got, nil
}

// Resolve records decidedBy's decision ("approved" or "denied") and comment on
// the pending request id in the caller's organization, returning the decided
// request. The organization comes from ctx: without that predicate a caller
// who learned another organization's request id could decide it.
func (s *Store) Resolve(ctx context.Context, id, decidedBy uuid.UUID, decision, comment string) (ApprovalRequest, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if decision != StateApproved && decision != StateDenied {
		return ApprovalRequest{}, fmt.Errorf("invalid decision %q", decision)
	}
	got, err := scanRequest(s.pool.QueryRow(ctx, `
		UPDATE approvals.approval_requests
		SET decision = $1, decided_by = $2, decided_at = now(), comment = $3
		WHERE id = $4 AND org_id = $5 AND decision = 'pending'
		RETURNING `+requestColumns,
		decision, decidedBy, comment, id, scope.OrgID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalRequest{}, ErrNotPending
	}
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("resolve approval %s: %w", id, err)
	}
	return got, nil
}

// Get returns one request in the caller's organization.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (ApprovalRequest, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return ApprovalRequest{}, err
	}
	got, err := scanRequest(s.pool.QueryRow(ctx, `
		SELECT `+requestColumns+` FROM approvals.approval_requests
		WHERE id = $1 AND org_id = $2`, id, scope.OrgID))
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("get approval %s: %w", id, err)
	}
	return got, nil
}

// Pending returns every approval request still awaiting a decision, scoped
// to the caller's org.
func (s *Store) Pending(ctx context.Context) ([]ApprovalRequest, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.list(ctx, `WHERE org_id = $1 AND decision = 'pending' ORDER BY created_at`, scope.OrgID)
}

// ForRun returns every request raised for runID in the caller's organization,
// newest first, in any state — what a run page shows as its approval history.
func (s *Store) ForRun(ctx context.Context, runID uuid.UUID) ([]ApprovalRequest, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.list(ctx, `WHERE org_id = $1 AND run_id = $2 ORDER BY created_at DESC`, scope.OrgID, runID)
}

func (s *Store) list(ctx context.Context, where string, args ...any) ([]ApprovalRequest, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+requestColumns+` FROM approvals.approval_requests `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	defer rows.Close()
	var out []ApprovalRequest
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan approval request: %w", err)
		}
		out = append(out, req)
	}
	return out, rows.Err()
}
