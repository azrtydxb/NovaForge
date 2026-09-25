package reviews

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// beginReviewExecution persists admission responsibility BEFORE any upstream
// call. Lease expiry fences a new invocation, never proves an old one stopped.
func (s *Store) beginReviewExecution(ctx context.Context, r reviewRequest, attempt uuid.UUID) error {
	if err := authz.RequireOrg(ctx, r.OrgID); err != nil {
		return err
	}
	if attempt == uuid.Nil {
		return fmt.Errorf("non-nil execution ID required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL synchronous_commit=on`); err != nil {
		return err
	}
	var id uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM reviews.agent_review_requests WHERE id=$1 AND org_id=$2 AND lease_id=$3 AND state='running' AND lease_until>now() FOR UPDATE`, r.ID, r.OrgID, r.LeaseID).Scan(&id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO reviews.review_executions(attempt_id,org_id,request_id,lease_id) VALUES($1,$2,$3,$4)`, attempt, r.OrgID, r.ID, r.LeaseID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// bindReviewExecution is a one-shot send fence. Even identical re-entry is
// refused: this consumer recovers only receipts, never replays a POST. A lost
// COMMIT acknowledgement or crash before send can leave an orphan forever.
func (s *Store) bindReviewExecution(ctx context.Context, r reviewRequest, attempt uuid.UUID, owner, digest string) error {
	if err := authz.RequireOrg(ctx, r.OrgID); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL synchronous_commit=on`); err != nil {
		return err
	}
	var id uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM reviews.agent_review_requests WHERE id=$1 AND org_id=$2 AND lease_id=$3 AND state='running' AND lease_until>now() FOR UPDATE`, r.ID, r.OrgID, r.LeaseID).Scan(&id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE reviews.review_executions SET owner_url=$5,request_digest=$6 WHERE attempt_id=$1 AND org_id=$2 AND request_id=$3 AND lease_id=$4 AND owner_url IS NULL AND terminated_at IS NULL`, attempt, r.OrgID, r.ID, r.LeaseID, owner, digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("execution binding missing or already assigned; not replayed")
	}
	return tx.Commit(ctx)
}

type outstandingExecution struct {
	AttemptID     uuid.UUID
	Owner, Digest string
}

// takeReviewExecutions rotates persisted check order before network I/O. Unknown
// old executions cannot permanently hide newer receipts, including after restart.
func (s *Store) takeReviewExecutions(ctx context.Context) ([]outstandingExecution, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return nil, fmt.Errorf("org scope required")
	}
	rows, err := s.pool.Query(ctx, `WITH due AS (
 SELECT attempt_id FROM reviews.review_executions WHERE org_id=$1 AND terminated_at IS NULL AND owner_url IS NOT NULL
 ORDER BY last_checked_at NULLS FIRST,created_at,attempt_id LIMIT 4 FOR UPDATE SKIP LOCKED)
 UPDATE reviews.review_executions e SET last_checked_at=clock_timestamp() FROM due
 WHERE e.attempt_id=due.attempt_id AND e.org_id=$1 RETURNING e.attempt_id,e.owner_url,e.request_digest`, scope.OrgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []outstandingExecution
	for rows.Next() {
		var e outstandingExecution
		if err = rows.Scan(&e.AttemptID, &e.Owner, &e.Digest); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// confirmReviewReceipt changes only admission metadata, never verdicts or usage.
// An expired/deleted request may retire its obligation but cannot gain approval.
func (s *Store) confirmReviewReceipt(ctx context.Context, e outstandingExecution, receipt executionReceipt) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return fmt.Errorf("org scope required")
	}
	if err = receipt.validate(e); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL synchronous_commit=on`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE reviews.review_executions SET terminated_at=now(),termination_evidence='gateway_receipt',receipt_version=$5,upstream_status=$6,completed_at=$7
 WHERE attempt_id=$1 AND org_id=$2 AND owner_url=$3 AND request_digest=$4 AND terminated_at IS NULL`, e.AttemptID, scope.OrgID, e.Owner, e.Digest, receipt.Version, receipt.UpstreamStatus, receipt.CompletedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		var confirmed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reviews.review_executions WHERE attempt_id=$1 AND org_id=$2 AND owner_url=$3 AND request_digest=$4 AND termination_evidence='gateway_receipt' AND completed_at=$5)`, e.AttemptID, scope.OrgID, e.Owner, e.Digest, receipt.CompletedAt).Scan(&confirmed)
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("execution receipt missing or mismatched")
		}
	}
	return tx.Commit(ctx)
}
