package agents_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
		SponsorId:   actorOf(ctx),
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

// TestStartRunRefusesUnapprovedProposal pins the enforcement half of
// maintenance approval. A proposal awaiting approval is an ordinary open Work
// Item to anything that does not ask, so an agent could be started against it
// — by a person clicking Start, or by any automation — and the fix executed
// with nobody having approved it.
func TestStartRunRefusesUnapprovedProposal(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	work := &stubWorkClient{item: &workv1.WorkItem{
		Id: uuid.New().String(), Key: "NF-11", RepoId: repoID.String(), State: "open",
		AwaitingApproval: true,
	}}
	srv, store := newGRPCServer(t, work)
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)

	_, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{
		AgentId:     agent.ID.String(),
		RepoId:      repoID.String(),
		WorkItemKey: "NF-11",
		SponsorId:   actorOf(ctx),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("StartRun on an unapproved proposal: code = %v, want FailedPrecondition", status.Code(err))
	}
	if stats, _ := store.ListStats(ctx); len(stats) != 0 {
		t.Fatalf("a run was created for an unapproved proposal: %+v", stats)
	}
}

// TestListRunsForWorkItemCarriesRuns pins what a screen offering Cancel needs:
// the runs against a Work Item with their states. The RPC returned only ids,
// so nothing could show which of them was still running without one GetRun
// per id.
func TestListRunsForWorkItemCarriesRuns(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	workItemID := uuid.New()
	work := &stubWorkClient{item: &workv1.WorkItem{
		Id: workItemID.String(), Key: "NF-10", RepoId: repoID.String(), State: "open",
	}}
	srv, store := newGRPCServer(t, work)
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)

	started, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{
		AgentId:     agent.ID.String(),
		RepoId:      repoID.String(),
		WorkItemKey: "NF-10",
		SponsorId:   actorOf(ctx),
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	resp, err := srv.ListRunsForWorkItem(ctx, &agentsv1.ListRunsForWorkItemRequest{WorkItemId: workItemID.String()})
	if err != nil {
		t.Fatalf("ListRunsForWorkItem: %v", err)
	}
	if len(resp.GetRuns()) != 1 {
		t.Fatalf("runs = %d, want 1", len(resp.GetRuns()))
	}
	run := resp.GetRuns()[0]
	if run.GetId() != started.GetRun().GetId() || run.GetState() == "" || run.GetAgentId() != agent.ID.String() {
		t.Fatalf("run = %+v, want the started run with its state and agent", run)
	}

	// Another organization asking about the same Work Item id sees nothing.
	other, err := srv.ListRunsForWorkItem(scopedCtx(uuid.New()), &agentsv1.ListRunsForWorkItemRequest{WorkItemId: workItemID.String()})
	if err != nil {
		t.Fatalf("ListRunsForWorkItem from another org: %v", err)
	}
	if len(other.GetRuns()) != 0 || len(other.GetRunIds()) != 0 {
		t.Fatalf("another organization saw %d runs", len(other.GetRuns()))
	}
}
