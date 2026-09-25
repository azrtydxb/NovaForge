package ci

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// StartClaimedJob marks execution only after credentials resolved. A runner
// disconnect can reap the reservation during that RPC; the conditional update
// must not resurrect that failed job when the late broker response arrives.
func (s *Store) StartClaimedJob(ctx context.Context, jobID, runnerID uuid.UUID, attempts ...uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin dispatch: %w", err)
	}
	defer tx.Rollback(ctx)
	var runID uuid.UUID
	if err := tx.QueryRow(ctx, `
		UPDATE ci.workflow_jobs
		SET status = 'running', started_at = now(), detail = '', not_before = NULL
		WHERE id = $1 AND runner_id = $2 AND status = 'pending'
		RETURNING run_id`, jobID, runnerID).Scan(&runID); err != nil {
		return fmt.Errorf("start claimed job: %w", err)
	}
	if len(attempts) > 0 && attempts[0] != uuid.Nil {
		tag, err := tx.Exec(ctx, `UPDATE ci.credential_cleanup c SET phase='dispatched' FROM ci.workflow_runs r WHERE r.id=$1 AND c.org_id=r.org_id AND c.job_id=$2 AND c.attempt_id=$3 AND c.phase='preparing' AND c.ready_at>now()`, runID, jobID, attempts[0])
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("credential preparation no longer dispatchable")
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ci.workflow_runs SET status = 'running' WHERE id = $1 AND status = 'queued'`, runID); err != nil {
		return fmt.Errorf("start claimed run: %w", err)
	}
	return tx.Commit(ctx)
}
