package agents_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
)

// TestCancelRunStopsExecution pins that CancelRun reaches the run it names
// when that run is executing in this process. CancelRun used to write
// "cancelled" and return, while the context Execute was running under stayed
// live — so an in-flight model call or tool call carried on for as long as it
// liked after the person pressing Cancel had been told it had stopped.
func TestCancelRunStopsExecution(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	work := &stubWorkClient{item: &workv1.WorkItem{
		Id: uuid.New().String(), Key: "NF-9", RepoId: repoID.String(), State: "open",
	}}
	srv, store := newGRPCServer(t, work)

	executing := make(chan struct{})
	stopped := make(chan struct{})
	srv.Execute = func(ctx context.Context, run agents.Run) {
		close(executing)
		<-ctx.Done()
		close(stopped)
	}

	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)
	started, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{
		AgentId:     agent.ID.String(),
		RepoId:      repoID.String(),
		WorkItemKey: "NF-9",
		SponsorId:   uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	select {
	case <-executing:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached Execute")
	}

	if _, err := srv.CancelRun(ctx, &agentsv1.CancelRunRequest{Id: started.GetRun().GetId()}); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("CancelRun returned but the executing run's context was never cancelled")
	}

	runID := uuid.MustParse(started.GetRun().GetId())
	got, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != "cancelled" {
		t.Fatalf("run state = %q, want cancelled", got.State)
	}
}
