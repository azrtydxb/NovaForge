package ci

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// StartClaimedJob marks execution only after credentials resolved. A runner
// disconnect can reap the reservation during that RPC; the conditional update
// must not resurrect that failed job when the late broker response arrives.
func (s *Store) StartClaimedJob(ctx context.Context, jobID, runnerID uuid.UUID) error {
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
	if _, err := tx.Exec(ctx, `
		UPDATE ci.workflow_runs SET status = 'running' WHERE id = $1 AND status = 'queued'`, runID); err != nil {
		return fmt.Errorf("start claimed run: %w", err)
	}
	return tx.Commit(ctx)
}
