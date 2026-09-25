package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/authz"
)

// OrphanGrace is how far past its wall-clock limit a running run must be
// before it is taken for orphaned. The loop's deadline is measured from when
// its budget was created, which is after the workspace was provisioned and
// seeded (up to several minutes after started_at), and a tool call cut off at
// the deadline still takes a moment to unwind and settle.
const OrphanGrace = 15 * time.Minute

// orphan identifies a run to recover and the organization it belongs to.
type orphan struct {
	OrgID   uuid.UUID
	RunID   uuid.UUID
	AgentID uuid.UUID
}

// staleRunning lists runs still "running" more than grace past their own
// wall-clock limit, across every organization.
//
// It is deliberately a query with no organization predicate, like
// work.Store.OpenEpics: recovery is a platform worker with no organization of
// its own. It returns only ids, and RecoverOrphanedRuns re-enters each run's
// organization before reading or writing anything of it.
func (s *Store) staleRunning(ctx context.Context, grace time.Duration) ([]orphan, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, id, agent_id FROM agents.agent_runs
		WHERE state = 'running'
		  AND started_at IS NOT NULL
		  AND started_at + make_interval(secs => wallclock_limit_seconds) + make_interval(secs => $1) < now()`,
		grace.Seconds())
	if err != nil {
		return nil, fmt.Errorf("list stale running runs: %w", err)
	}
	defer rows.Close()
	var out []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.OrgID, &o.RunID, &o.AgentID); err != nil {
			return nil, fmt.Errorf("scan stale run: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RecoverOrphanedRuns settles, as failed, every run that is still "running"
// more than grace past its wall-clock limit, and reports how many it settled.
//
// A run is driven to its end by the agent-runtime replica executing it. When
// that replica dies — a crash, an eviction, a deploy that did not drain — the
// row stays "running" with nothing behind it. Because the branch lock is the
// run being running, the agent's branch would then be locked against every
// person forever. The deadline fences further intended work, but elapsed time
// is not process-death evidence: Work release waits for a completion receipt.
func RecoverOrphanedRuns(ctx context.Context, store *Store, rdb *redis.Client, grace time.Duration) (int, error) {
	// The platform scan returns IDs only; every mutation re-enters the org
	// and repeats eligibility under that predicate. It never sweeps shared state
	// under an invented organization or overwrites a healthy admission lease.
	rows, err := store.pool.Query(ctx, `SELECT org_id,id,agent_id FROM agents.agent_runs
 WHERE state='queued' AND grant_issue_intent IS NOT NULL AND admission_lease_until<now()
 ORDER BY admission_lease_until,id LIMIT 100`)
	if err != nil {
		return 0, err
	}
	var expired []orphan
	for rows.Next() {
		var o orphan
		if err = rows.Scan(&o.OrgID, &o.RunID, &o.AgentID); err != nil {
			rows.Close()
			return 0, err
		}
		expired = append(expired, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, o := range expired {
		scoped := authz.WithScope(ctx, authz.Scope{OrgID: o.OrgID, ActorID: o.AgentID, ActorKind: "agent"})
		if _, err = store.pool.Exec(scoped, `UPDATE agents.agent_runs SET state='failed',ended_at=now(),end_reason='admission replica lost before execution',grant_cleanup_pending=grant_id <> '00000000-0000-0000-0000-000000000000'
 WHERE id=$1 AND org_id=$2 AND state='queued' AND grant_issue_intent IS NOT NULL AND admission_lease_until<now()`, o.RunID, o.OrgID); err != nil {
			return 0, err
		}
	}

	stale, err := store.staleRunning(ctx, grace)
	if err != nil {
		return 0, err
	}
	var settled int
	var errs []error
	for _, o := range stale {
		orgCtx := authz.WithScope(ctx, authz.Scope{OrgID: o.OrgID, ActorID: o.AgentID, ActorKind: "agent"})
		reason := fmt.Sprintf("no agent-runtime replica finished this run: it was still running %s past its wall-clock limit, execution termination and final accounting are unconfirmed; it was settled failed, authority cleanup scheduled and its branch released", grace)
		// A stale scan is not authority to overwrite a completion that won the
		// row race. Recovery never invents zero spend or replaces known counters.
		tag, err := store.pool.Exec(orgCtx, `UPDATE agents.agent_runs SET state='failed',ended_at=now(),end_reason=$3,
   grant_cleanup_pending=grant_id <> '00000000-0000-0000-0000-000000000000'
   WHERE id=$1 AND org_id=$2 AND state='running' AND completion IS NULL
   AND started_at + make_interval(secs => wallclock_limit_seconds) + make_interval(secs => $4) < now()`, o.RunID, o.OrgID, reason, grace.Seconds())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		publishStateChange(rdb, o.RunID, "running", "failed")
		settled++
	}
	if err := store.ReconcileGrantCleanup(ctx); err != nil {
		errs = append(errs, err)
	}
	return settled, errors.Join(errs...)
}
