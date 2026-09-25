package agents_test

import (
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/workspace"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestWorkspacePendingSurvivesRestartAndBlocksPurge(t *testing.T) {
	store := agents.NewStore(storePool(t))
	org, repo := uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	run, err := store.CreateRun(ctx, agents.Run{OrgID: org, RepoID: repo, AgentID: agent.ID, SponsorID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	identity := workspace.Identity{OrgID: org, RunID: run.ID, NamespaceUID: "owned-namespace", PodUID: "owned-pod", NodeName: "node"}
	if err = store.RecordWorkspaceCleanup(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteRun(ctx, run.ID, agents.Completion{State: "succeeded"}); err == nil {
		t.Fatal("success while teardown pending")
	}
	if _, err = store.CompleteRun(ctx, run.ID, agents.Completion{State: "failed"}); err != nil {
		t.Fatal(err)
	}
	restart := agents.NewStore(store.Pool())
	if err = restart.CleanupRunGrant(ctx, run.ID); err == nil {
		t.Fatal("missing cleaner accepted")
	}
	if _, err = restart.PurgeRuns(ctx, &repo); err == nil {
		t.Fatal("purged unconfirmed workspace")
	}
	saved, err := restart.GetRun(ctx, run.ID)
	if err != nil || !saved.WorkspaceCleanupPending || saved.WorkspaceCleanupError == "" {
		t.Fatalf("pending lost: %+v %v", saved, err)
	}
	if err = restart.ConfirmWorkspaceCleanup(ctx, run.ID); err == nil {
		t.Fatal("absence invented termination")
	}
	replacement := identity
	replacement.PodUID = "replacement"
	if err = restart.RecordWorkspaceCleanup(ctx, replacement); err == nil {
		t.Fatal("UID replaced")
	}
	restart.WorkspaceCleaner = workspace.NewProvisioner(fake.NewClientset()).DestroyConfirmed
	identity.TerminationObserved = true
	if err = restart.RecordWorkspaceCleanup(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if err = restart.ConfirmWorkspaceCleanup(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = restart.PurgeRuns(ctx, &repo); err != nil {
		t.Fatal(err)
	}
}
