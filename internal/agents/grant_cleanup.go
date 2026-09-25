package agents

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// CleanupRunGrant confirms a terminal run's durable cleanup intent. Terminal
// state fences model work immediately, but it does not claim remote authority
// was revoked: pending stays true until the owner confirms the exact grant.
// No agent-schema transaction is held across this service boundary.
func (s *Store) CleanupRunGrant(ctx context.Context, id uuid.UUID) error {
	return s.cleanupRunGrant(ctx, id, uuid.Nil)
}

func (s *Store) cleanupRunGrant(ctx context.Context, id, claim uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if claim == uuid.Nil {
		claim = uuid.New()
		tag, err := s.pool.Exec(ctx, `UPDATE agents.agent_runs SET grant_cleanup_claim=$1,
   grant_cleanup_lease_until=clock_timestamp()+interval '2 minutes',grant_cleanup_attempts=grant_cleanup_attempts+1
   WHERE id=$2 AND org_id=$3 AND state IN ('failed','cancelled','succeeded','over_budget')
   AND (grant_cleanup_pending OR work_release_pending OR workspace_cleanup_pending) AND grant_cleanup_lease_until<=clock_timestamp()`, claim, id, scope.OrgID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			current, err := s.GetRun(ctx, id)
			if err != nil {
				return err
			}
			if terminalStates[current.State] && !current.GrantCleanupPending && !current.WorkReleasePending && !current.WorkspaceCleanupPending {
				return nil
			}
			return fmt.Errorf("run cleanup already claimed or not terminal; retry required")
		}
	}
	// Read after claiming: a stale scan must not replay a release based on an
	// earlier outcome or overwrite another replica's confirmed obligations.
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return err
	}
	var owns bool
	if err = s.pool.QueryRow(ctx, `SELECT grant_cleanup_claim=$3 AND grant_cleanup_lease_until>clock_timestamp() FROM agents.agent_runs WHERE id=$1 AND org_id=$2`, id, scope.OrgID, claim).Scan(&owns); err != nil {
		return err
	}
	if !owns {
		return fmt.Errorf("cleanup claim expired")
	}

	var revokeErr, releaseErr, workspaceErr error
	if run.GrantCleanupPending {
		if run.GrantIntent != nil {
			if s.GrantIssuanceCanceller == nil {
				revokeErr = fmt.Errorf("issuance cancellation authority unavailable")
			} else {
				revokeErr = s.GrantIssuanceCanceller(ctx, *run.GrantIntent)
			}
		} else if s.GrantRevoker == nil {
			revokeErr = fmt.Errorf("grant revoker unavailable")
		} else {
			revokeErr = s.GrantRevoker(ctx, run.GrantID)
		}
	}
	if run.WorkspaceCleanupPending {
		if s.WorkspaceCleaner == nil || run.WorkspaceIdentity == nil {
			workspaceErr = fmt.Errorf("workspace cleanup unavailable")
		} else {
			workspaceCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			workspaceErr = s.WorkspaceCleaner(workspaceCtx, *run.WorkspaceIdentity)
			stop()
		}
	}

	if run.WorkReleasePending {
		switch {
		case workspaceErr != nil:
			releaseErr = fmt.Errorf("workspace termination unconfirmed")
		case revokeErr != nil:
			releaseErr = fmt.Errorf("authority not yet fenced")
		case !run.StartedAt.IsZero() && !run.ExecutionFinished:
			releaseErr = fmt.Errorf("execution has not confirmed termination")
		case s.WorkClaims == nil:
			releaseErr = fmt.Errorf("Work execution authority unavailable")
		default:
			outcome := run.State
			if run.StartedAt.IsZero() {
				outcome = "admission_failed"
			}
			releaseErr = s.WorkClaims.ReleaseExecution(ctx, ExecutionRelease{ExecutionClaim: run.ExecutionClaim(), Outcome: outcome})
		}
	}

	// A timeout must not prevent recording the pending condition. Keep the
	// error generic: owner errors can contain credentials or upstream URLs.
	writeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer stop()
	detail := ""
	if revokeErr != nil {
		detail = "grant revocation not confirmed; cleanup will retry"
	}
	workspaceDetail := ""
	if workspaceErr != nil {
		workspaceDetail = "workspace termination unconfirmed; cleanup will retry"
	}
	releaseDetail := ""
	if releaseErr != nil {
		releaseDetail = "Work execution release unconfirmed; cleanup will retry"
	}
	ack, writeErr := s.pool.Exec(writeCtx, `UPDATE agents.agent_runs SET grant_cleanup_pending=$1, grant_cleanup_error=$2, work_release_pending=$7,work_release_error=$8,workspace_cleanup_pending=$9,workspace_cleanup_error=$10,
 grant_cleanup_next_at=clock_timestamp()+interval '1 second', grant_cleanup_lease_until='-infinity', grant_cleanup_claim=NULL
 WHERE id=$3 AND org_id=$4 AND grant_id=$5 AND (grant_cleanup_pending OR work_release_pending OR workspace_cleanup_pending) AND grant_cleanup_claim=$6`, revokeErr != nil, detail, id, run.OrgID, run.GrantID, claim, releaseErr != nil, releaseDetail, workspaceErr != nil, workspaceDetail)
	if writeErr != nil {
		return writeErr
	}
	if ack.RowsAffected() == 0 {
		// A successful remote call is not a successful durable acknowledgement.
		// Another replica may have taken over, or already cleared everything.
		current, err := s.GetRun(writeCtx, id)
		if err != nil {
			return err
		}
		if current.GrantCleanupPending || current.WorkReleasePending || current.WorkspaceCleanupPending {
			return fmt.Errorf("run %s: cleanup claim lost; retry required", id)
		}
		return nil
	}
	if revokeErr != nil || releaseErr != nil || workspaceErr != nil {
		return fmt.Errorf("run %s: resource cleanup pending", id)
	}
	return nil
}

// ReconcileGrantCleanup is a platform worker scan returning only ids, like
// staleRunning. It re-enters each organization before reading a run or calling
// the grant owner. A bounded batch includes terminal rows, not only orphans.
func (s *Store) ReconcileGrantCleanup(ctx context.Context) error {
	claim := uuid.New()
	// Atomic bounded claiming prevents replica fanout over the same oldest rows.
	// Lease covers the entire bounded worker batch; crashed replicas relinquish it.
	rows, err := s.pool.Query(ctx, `WITH due AS (
  SELECT id FROM agents.agent_runs WHERE (grant_cleanup_pending OR work_release_pending OR workspace_cleanup_pending)
  AND state IN ('failed','cancelled','succeeded','over_budget') AND grant_cleanup_next_at<=clock_timestamp()
  AND grant_cleanup_lease_until<=clock_timestamp() ORDER BY grant_cleanup_next_at,ended_at,id LIMIT 100 FOR UPDATE SKIP LOCKED
 ) UPDATE agents.agent_runs r SET grant_cleanup_claim=$1,grant_cleanup_lease_until=clock_timestamp()+interval '2 minutes',
 grant_cleanup_attempts=grant_cleanup_attempts+1 FROM due WHERE r.id=due.id RETURNING r.org_id,r.id,r.agent_id`, claim)
	if err != nil {
		return err
	}
	var pending []orphan
	for rows.Next() {
		var item orphan
		if err := rows.Scan(&item.OrgID, &item.RunID, &item.AgentID); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Ten independent bounded owner calls cap one sweep at roughly ninety seconds,
	// rather than serially holding newer grants behind a hundred owner timeouts.
	var errs []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan orphan)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				scoped := authz.WithScope(ctx, authz.Scope{OrgID: item.OrgID, ActorID: item.AgentID, ActorKind: "agent"})
				if err := s.cleanupRunGrant(scoped, item.RunID, claim); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}()
	}
	for _, item := range pending {
		if ctx.Err() != nil {
			break
		}
		jobs <- item
	}
	close(jobs)
	wg.Wait()
	return errors.Join(append(errs, ctx.Err())...)
}
