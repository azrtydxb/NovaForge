package agents_test

import (
	"context"
	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"testing"
)

func TestPurgeFencesLateAdmission(t *testing.T) {
	store := agents.NewStore(storePool(t))
	org, repo := uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	if _, err := store.PurgeRuns(ctx, &repo); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, agents.Run{OrgID: org, RepoID: repo, AgentID: agent.ID, SponsorID: uuid.New()}); err == nil {
		t.Fatal("new run admitted after purge final check")
	}
}
func TestStartRejectsUnavailableExecutorBeforeGrant(t *testing.T) {
	org, repo := uuid.New(), uuid.New()
	srv, store := newGRPCServer(t, &stubWorkClient{item: &workv1.WorkItem{Id: uuid.NewString(), Key: "NF-1", RepoId: repo.String()}})
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	srv.Execute = nil
	_, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{AgentId: agent.ID.String(), RepoId: repo.String(), SponsorId: actorOf(ctx), WorkItemKey: "NF-1"})
	if err == nil {
		t.Fatal("nil executor issued grant and admitted run")
	}
	var count int
	if err = store.Pool().QueryRow(context.Background(), `SELECT count(*) FROM agents.agent_runs WHERE org_id=$1`, org).Scan(&count); err != nil || count != 0 {
		t.Fatalf("admitted %d rows: %v", count, err)
	}
}
