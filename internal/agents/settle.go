package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
)

// SettleRun writes a finished run's terminal state and publishes the change
// on the agent event stream. It is the one path every ending takes — a loop
// that finished, a workspace that could not be provisioned, an orphaned run
// recovered after a crash — so they cannot disagree about what "ended" means.
//
// Ending a run is also what releases its branch lock: the lock is the run
// being "running" (see BranchLock), so there is no second record to forget.
//
// rdb may be nil, in which case nothing is published.
func SettleRun(ctx context.Context, store *Store, rdb *redis.Client, run Run, state string) error {
	// CancelRun already wrote "cancelled" and published it, and the state
	// machine refuses cancelled -> cancelled.
	if state == "cancelled" {
		return nil
	}
	if !terminalStates[state] {
		return fmt.Errorf("settle run %s: %q is not a terminal state", run.ID, state)
	}
	// The run's own context is cancelled when the run is, and a state write
	// under it would fail — so the terminal write detaches from it, acting as
	// the run's agent inside the run's organization.
	scoped := authz.WithScope(context.WithoutCancel(ctx), authz.Scope{OrgID: run.OrgID, ActorID: run.AgentID, ActorKind: "agent"})
	if err := store.SetRunState(scoped, run.ID, state); err != nil {
		return fmt.Errorf("settle run %s as %s: %w", run.ID, state, err)
	}
	publishStateChange(rdb, run.ID, "running", state)
	return nil
}

// publishStateChange puts one run state change on the agent event stream,
// best-effort: the row is the authority, the stream is how watchers hear.
func publishStateChange(rdb *redis.Client, runID uuid.UUID, from, to string) {
	if rdb == nil {
		return
	}
	_ = events.Publish(context.Background(), rdb, events.StreamAgentEvents, events.AgentEvent{
		RunID: runID, At: time.Now(), Type: "state_change", FromState: from, ToState: to,
	})
}
