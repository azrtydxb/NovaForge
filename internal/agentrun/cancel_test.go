package agentrun_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/tools"
)

// cancellingModel plays back its script, and on its first call cancels the
// run exactly as CancelRun does — a cancellation arriving while the loop is
// mid-run, possibly on another agent-runtime replica that has no handle on
// this loop at all.
type cancellingModel struct {
	*stubModel
	cancel func()
	once   bool
}

func (m *cancellingModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	if !m.once {
		m.once = true
		m.cancel()
	}
	if err := ctx.Err(); err != nil {
		// A real gateway call aborts when its context is cancelled.
		return nil, err
	}
	return m.stubModel.Generate(ctx, call)
}

func runningRun(t *testing.T) (*agents.AuditLog, *agents.Store, context.Context, agents.Run) {
	t.Helper()
	audit := testAuditPool(t)
	store := newTestStore(t, audit)
	orgID := uuid.New()
	ctx := scopedTestCtx(orgID)
	run := newAgentRun(t, ctx, store, orgID)
	if err := store.SetRunState(ctx, run.ID, "running"); err != nil {
		t.Fatalf("SetRunState running: %v", err)
	}
	return audit, store, ctx, run
}

// TestLoopStopsWhenRunCancelled pins the seam between CancelRun and the
// executing loop. CancelRun only moved the row to "cancelled"; the loop never
// looked, so a cancelled agent kept calling tools — committing to its branch —
// until the model stopped on its own, and then tried to record "succeeded".
// The loop must stop at its next step boundary and report the run cancelled.
func TestLoopStopsWhenRunCancelled(t *testing.T) {
	audit, store, ctx, run := runningRun(t)

	model := &cancellingModel{
		stubModel: &stubModel{script: alwaysCallTool(5, "work.get", 1)},
		cancel: func() {
			if err := store.SetRunState(ctx, run.ID, "cancelled"); err != nil {
				t.Errorf("SetRunState cancelled: %v", err)
			}
		},
	}
	budget := agents.NewBudget(time.Hour, 1_000_000, 1_000_000)
	reg := tools.NewRegistry(tools.Runtime{
		Budget: budget,
		Grant:  capability.Grant{WriteBranch: "agents/NF-1/"},
		Work:   &fakeWorkClient{},
	}, audit)

	loop := agentrun.NewLoop(model, budget, audit)
	loop.Runs = store
	result, err := loop.Execute(ctx, run, reg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.State != "cancelled" {
		t.Fatalf("State = %q, want %q: the loop ran on after its run was cancelled", result.State, "cancelled")
	}
	if model.calls != 1 {
		t.Fatalf("model.calls = %d, want 1: the loop kept asking the model for work after cancellation", model.calls)
	}

	entries, err := audit.List(ctx, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.Tool == "work.get" {
			t.Fatal("a tool was called after the run was cancelled")
		}
	}

	got, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != "cancelled" {
		t.Fatalf("run state = %q, want it to stay cancelled", got.State)
	}
}

// TestLoopCancelledContextReportsCancelled pins the in-process half: when
// CancelRun lands on the replica executing the run it cancels the run's
// context, which aborts an in-flight model call. That abort must be reported
// as the cancellation it is, not as a failed model call.
func TestLoopCancelledContextReportsCancelled(t *testing.T) {
	audit, store, ctx, run := runningRun(t)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := &cancellingModel{
		stubModel: &stubModel{script: alwaysCallTool(5, "work.get", 1)},
		cancel: func() {
			if err := store.SetRunState(ctx, run.ID, "cancelled"); err != nil {
				t.Errorf("SetRunState cancelled: %v", err)
			}
			cancel()
		},
	}
	budget := agents.NewBudget(time.Hour, 1_000_000, 1_000_000)
	reg := tools.NewRegistry(tools.Runtime{
		Budget: budget,
		Grant:  capability.Grant{WriteBranch: "agents/NF-1/"},
		Work:   &fakeWorkClient{},
	}, audit)

	loop := agentrun.NewLoop(model, budget, audit)
	loop.Runs = store
	result, err := loop.Execute(runCtx, run, reg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.State != "cancelled" {
		t.Fatalf("State = %q, want %q", result.State, "cancelled")
	}
}
