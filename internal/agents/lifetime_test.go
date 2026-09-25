package agents_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
)

func TestRunLifetimeValidatesBeforeConversion(t *testing.T) {
	for _, seconds := range []int64{-1, math.MaxInt64, math.MaxInt64 / int64(time.Second), 86400} {
		if _, err := agents.RunWallclockLimit(seconds); err == nil {
			t.Errorf("accepted unsafe lifetime %d", seconds)
		}
	}
	if got, err := agents.RunWallclockLimit(0); err != nil || got != time.Hour {
		t.Fatalf("default=%v %v", got, err)
	}
	if got, err := agents.RunWallclockLimit(85500); err != nil || got != 24*time.Hour-agents.OrphanGrace {
		t.Fatalf("maximum=%v %v", got, err)
	}
}
func TestExecutionDeadlineIncludesProvisioning(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	srv, store := newGRPCServer(t, &stubWorkClient{item: &workv1.WorkItem{Id: uuid.NewString(), Key: "NF-98", RepoId: repoID.String()}})
	done := make(chan error, 1)
	srv.Execute = func(ctx context.Context, run agents.Run) {
		if run.StartedAt.IsZero() {
			done <- errors.New("no durable start time")
			return
		}
		<-ctx.Done()
		done <- context.Cause(ctx)
	}
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)
	_, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{AgentId: agent.ID.String(), RepoId: repoID.String(), WorkItemKey: "NF-98", SponsorId: actorOf(ctx), WallclockLimitSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case cause := <-done:
		if !errors.Is(cause, agents.ErrOverBudget) {
			t.Fatalf("deadline cause=%v", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provisioning was not covered by run deadline")
	}
}
