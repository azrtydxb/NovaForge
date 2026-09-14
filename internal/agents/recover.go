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
// person forever. No loop can legitimately still be executing such a run: the
// loop enforces the wall-clock limit as a deadline on every call.
func RecoverOrphanedRuns(ctx context.Context, store *Store, rdb *redis.Client, grace time.Duration) (int, error) {
	stale, err := store.staleRunning(ctx, grace)
	if err != nil {
		return 0, err
	}
	var settled int
	var errs []error
	for _, o := range stale {
		orgCtx := authz.WithScope(ctx, authz.Scope{OrgID: o.OrgID, ActorID: o.AgentID, ActorKind: "agent"})
		reason := fmt.Sprintf("no agent-runtime replica finished this run: it was still running %s past its wall-clock limit, so the replica executing it is gone; it was settled failed and its branch released", grace)
		if err := store.RecordSpend(orgCtx, o.RunID, Spend{Reason: reason}); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := SettleRun(orgCtx, store, rdb, Run{ID: o.RunID, OrgID: o.OrgID, AgentID: o.AgentID}, "failed"); err != nil {
			// Another replica settled it first; that is the outcome wanted.
			errs = append(errs, err)
			continue
		}
		settled++
	}
	return settled, errors.Join(errs...)
}
