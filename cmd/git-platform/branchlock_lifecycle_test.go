package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/platformtest"
)

// Check both owners: a terminal state alone is not proof that execution ended
// or that Work released the claim. Read the runtime through its own store and
// Work through its authenticated API; never manufacture cleanup flags in SQL.
func assertBranchLockLifecycle(t *testing.T, p *platformtest.Platform, runtimeCtx, workCtx context.Context, runID uuid.UUID, itemID string, finished bool) {
	t.Helper()
	run, err := p.AgentStore.GetRun(runtimeCtx, runID)
	if err != nil {
		t.Fatalf("read run lifecycle: %v", err)
	}
	wantState := "running"
	if finished {
		wantState = "cancelled"
	}
	if run.State != wantState || run.ExecutionFinished != finished || !run.WorkClaimRequired || run.WorkReleasePending == finished {
		t.Fatalf("run lifecycle: state=%q finished=%v claimRequired=%v releasePending=%v, want %q finished=%v releasePending=%v",
			run.State, run.ExecutionFinished, run.WorkClaimRequired, run.WorkReleasePending, wantState, finished, !finished)
	}
	if finished && (run.GrantCleanupPending || run.WorkspaceCleanupPending) {
		t.Fatalf("run cleanup unconfirmed: grant=%v workspace=%v", run.GrantCleanupPending, run.WorkspaceCleanupPending)
	}
	item, err := p.Work.GetItem(workCtx, &workv1.GetItemRequest{Id: itemID})
	if err != nil {
		t.Fatalf("read Work execution claim: %v", err)
	}
	// execution_claimed is historical admission, not the current lock. Work's
	// owner changes in_progress to blocked when a cancelled run is released.
	wantWorkState := "in_progress"
	if finished {
		wantWorkState = "blocked"
	}
	if !item.GetItem().GetExecutionClaimed() || item.GetItem().GetState() != wantWorkState {
		t.Fatalf("Work execution claimed=%v state=%q, want historical claim and %q",
			item.GetItem().GetExecutionClaimed(), item.GetItem().GetState(), wantWorkState)
	}
}
