package swarm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/work"
)

// defaultMaxConcurrentRuns is used when Scheduler.MaxConcurrentRuns is
// unset, matching the repository configuration key
// project.max_concurrent_runs' documented default.
const defaultMaxConcurrentRuns = 5

// tickInterval is how often Scheduler.Run ticks every open epic.
const tickInterval = 30 * time.Second

// Scheduler starts an Agent Run for every ready subtask of an epic whose
// agent role maps to an enabled agent, respecting a per-epic concurrency
// cap. It never bypasses Task 1's Ready query or Task 3's claim-on-tick
// compare-and-swap: those are what make two concurrent ticks unable to
// start the same subtask twice.
type Scheduler struct {
	Work *work.Store

	// MaxConcurrentRuns caps how many of one epic's subtasks may be
	// in_progress at once. Zero means defaultMaxConcurrentRuns (5); callers
	// resolve the repository's project.max_concurrent_runs configuration
	// (or its default) into this field before calling Tick.
	MaxConcurrentRuns int

	// RoleAgent maps an agent role to the enabled agent that should run
	// subtasks assigned to it. A role with no entry here has no agent able
	// to pick up its subtasks, so subtasks naming it are left open rather
	// than started. Production wiring builds this from
	// agents.Store.ListAgents (filtered to Enabled); tests supply it
	// directly.
	RoleAgent map[string]agents.Agent

	// StartRun starts an Agent Run for a subtask already claimed
	// (transitioned open -> in_progress) and assigned to agent. Production
	// wiring creates the run through agents.Store.CreateRun and hands it to
	// the agent-runtime loop; tests record calls with a stub. A nil
	// StartRun makes Tick fail every claim it makes — it is a required
	// dependency, not an optional one.
	StartRun func(ctx context.Context, subtask work.Item, agent agents.Agent) (agents.Run, error)

	// RunOutcome reports the state of the Agent Run behind an in-progress
	// subtask. Tick uses it to block a subtask whose run ended without
	// succeeding. Production wiring asks agent-runtime.
	RunOutcome func(ctx context.Context, subtask work.Item) (string, error)
}

// concurrencyCap resolves sch.MaxConcurrentRuns, defaulting to
// defaultMaxConcurrentRuns.
func (sch *Scheduler) concurrencyCap() int {
	if sch.MaxConcurrentRuns > 0 {
		return sch.MaxConcurrentRuns
	}
	return defaultMaxConcurrentRuns
}

// Tick starts an Agent Run for every ready subtask of epicID whose role
// maps to an enabled agent in sch.RoleAgent, up to the concurrency cap. It
// also propagates a "blocked" child subtask up to the epic itself: an epic
// with any blocked child is marked blocked, so a failed prerequisite is
// visible on the epic without a human having to inspect every subtask.
//
// Claiming happens through work.Store.ClaimOpen's
// UPDATE ... WHERE state='open' RETURNING id compare-and-swap, so two
// concurrent Tick calls (or two calls to Tick with no state change between
// them) can never start the same subtask twice: the second claim attempt
// simply finds nothing to claim.
func (sch *Scheduler) Tick(ctx context.Context, epicID uuid.UUID) (started int, err error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return 0, err
	}

	children, err := sch.Work.Children(ctx, scope.OrgID, epicID)
	if err != nil {
		return 0, fmt.Errorf("swarm: tick epic %s: %w", epicID, err)
	}

	var errs []error
	inProgress := 0
	anyBlocked := false
	for _, c := range children {
		if c.State == "in_progress" {
			switch outcome := sch.runOutcome(ctx, c, &errs); {
			case failedRunStates[outcome]:
				// Its Agent Run ended without succeeding. Blocked is what keeps
				// every dependent out of Ready (which waits for "done") and what
				// surfaces the failure on the epic below; in_progress would
				// leave the dependents waiting on work nobody is doing.
				moved, err := sch.Work.TransitionState(ctx, c.ID, "in_progress", "blocked")
				if err != nil {
					errs = append(errs, fmt.Errorf("block subtask %s: %w", c.Key, err))
				} else if moved {
					c.State = "blocked"
				}
			case outcome == "succeeded":
				// A run only succeeds once it has been verified against the
				// subtask's acceptance criteria (agentrun's verification step),
				// so its success is what finishes the subtask. Nothing did this,
				// and every dependent of a successful subtask waited forever
				// for someone to close it by hand.
				moved, err := sch.Work.TransitionState(ctx, c.ID, "in_progress", "done")
				if err != nil {
					errs = append(errs, fmt.Errorf("finish subtask %s: %w", c.Key, err))
				} else if moved {
					c.State = "done"
				}
			}
		}
		switch c.State {
		case "in_progress":
			inProgress++
		case "blocked":
			anyBlocked = true
		}
	}
	if anyBlocked {
		if err := sch.Work.SetState(ctx, epicID, "blocked"); err != nil {
			return 0, fmt.Errorf("swarm: mark epic %s blocked: %w", epicID, err)
		}
	}

	ready, err := sch.Work.Ready(ctx, scope.OrgID, epicID)
	if err != nil {
		return 0, fmt.Errorf("swarm: tick epic %s: %w", epicID, err)
	}

	maxRuns := sch.concurrencyCap()
	for _, item := range ready {
		if inProgress+started >= maxRuns {
			break
		}

		role, err := sch.Work.SubtaskRole(ctx, item.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("subtask %s: %w", item.Key, err))
			continue
		}
		agent, ok := sch.RoleAgent[role]
		if !ok {
			// No enabled agent for this role: leave the subtask open for a
			// later tick, once an agent for that role exists.
			continue
		}

		claimed, err := sch.Work.ClaimOpen(ctx, item.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("claim subtask %s: %w", item.Key, err))
			continue
		}
		if !claimed {
			// Raced with a concurrent tick (or the item was claimed
			// between Ready and here); nothing to do.
			continue
		}

		if sch.StartRun == nil {
			errs = append(errs, fmt.Errorf("swarm: no StartRun configured, cannot start subtask %s", item.Key))
			if revertErr := sch.Work.SetState(ctx, item.ID, "open"); revertErr != nil {
				errs = append(errs, revertErr)
			}
			continue
		}
		if _, err := sch.StartRun(ctx, item, agent); err != nil {
			errs = append(errs, fmt.Errorf("start run for subtask %s: %w", item.Key, err))
			if revertErr := sch.Work.SetState(ctx, item.ID, "open"); revertErr != nil {
				errs = append(errs, revertErr)
			}
			continue
		}

		started++
	}

	return started, errors.Join(errs...)
}

// failedRunStates are the ways an Agent Run ends without doing its subtask.
var failedRunStates = map[string]bool{"failed": true, "over_budget": true, "cancelled": true}

// runOutcome reports the state of subtask's Agent Run. An unanswerable
// question is recorded and answered "": a subtask is only blocked or finished
// on evidence of how its run ended, never because agent-runtime was briefly
// unreachable.
func (sch *Scheduler) runOutcome(ctx context.Context, subtask work.Item, errs *[]error) string {
	if sch.RunOutcome == nil {
		return ""
	}
	state, err := sch.RunOutcome(ctx, subtask)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("read run outcome of subtask %s: %w", subtask.Key, err))
		return ""
	}
	return state
}

// EpicLister returns every epic (a top-level Work Item that has at least
// one materialised subtask) across every organization that Scheduler.Run
// should tick. Production wiring backs this with a store-level query
// scoped per org as it iterates organizations; tests supply a stub scoped
// to whatever fixture orgs they set up.
type EpicLister func(ctx context.Context) ([]struct {
	OrgID  uuid.UUID
	EpicID uuid.UUID
}, error)

// Run ticks every open epic on a 30s interval until ctx is cancelled. Each
// tick's error (if any) is reported through onTickError rather than
// stopping the loop, since one epic's scheduling trouble must not stall
// every other epic's — the same "isolate one failure from the rest"
// principle Task 5's scanners use.
func (sch *Scheduler) Run(ctx context.Context, epics EpicLister, onTickError func(epicID uuid.UUID, err error)) error {
	if epics == nil {
		return fmt.Errorf("swarm: Scheduler.Run requires an EpicLister")
	}
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			list, err := epics(ctx)
			if err != nil {
				if onTickError != nil {
					onTickError(uuid.Nil, err)
				}
				continue
			}
			for _, e := range list {
				tickCtx := authz.WithScope(ctx, authz.Scope{OrgID: e.OrgID, ActorKind: "system"})
				if _, err := sch.Tick(tickCtx, e.EpicID); err != nil && onTickError != nil {
					onTickError(e.EpicID, err)
				}
			}
		}
	}
}
