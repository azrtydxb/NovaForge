package ci_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
)

func TestDispatchReachesMatchingRunner(t *testing.T) {
	d := ci.NewDispatcher(nil)

	linuxID := uuid.New()
	linuxCh := make(chan *civ1.ConnectResponse, 1)
	d.Register(linuxID, []string{"linux"}, linuxCh)

	windowsID := uuid.New()
	windowsCh := make(chan *civ1.ConnectResponse, 1)
	d.Register(windowsID, []string{"windows"}, windowsCh)

	job := ci.DispatchJob{JobID: uuid.New(), RunID: uuid.New(), RunCmd: "go test ./..."}
	if err := d.Dispatch(context.Background(), job, []string{"linux"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	select {
	case msg := <-linuxCh:
		if msg.JobId != job.JobID.String() {
			t.Fatalf("want job id %s, got %s", job.JobID, msg.JobId)
		}
	default:
		t.Fatal("want linux runner to receive the job")
	}

	select {
	case msg := <-windowsCh:
		t.Fatalf("want windows runner to receive nothing, got %+v", msg)
	default:
	}
}

func TestDispatchNoMatchingRunner(t *testing.T) {
	d := ci.NewDispatcher(nil)

	windowsID := uuid.New()
	windowsCh := make(chan *civ1.ConnectResponse, 1)
	d.Register(windowsID, []string{"windows"}, windowsCh)

	job := ci.DispatchJob{JobID: uuid.New(), RunID: uuid.New(), RunCmd: "go test ./..."}
	err := d.Dispatch(context.Background(), job, []string{"linux"})
	if err == nil {
		t.Fatal("want error when no runner matches the required labels")
	}
	if !strings.Contains(err.Error(), "no runner") {
		t.Fatalf("want error containing %q, got %q", "no runner", err.Error())
	}
}

func TestBrokenStreamOrphansJob(t *testing.T) {
	pool := ciPool(t)
	store := ci.NewStore(pool)
	d := ci.NewDispatcher(store)

	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)
	run, created, err := store.CreateRun(ctx, ci.Run{OrgID: orgID, RepoID: repoID, CommitSHA: uuid.NewString(), Ref: "refs/heads/main"})
	if err != nil || !created {
		t.Fatalf("CreateRun: run=%+v created=%v err=%v", run, created, err)
	}
	job, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "test", RunCmd: "go test ./..."})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	runnerID := uuid.New()
	ch := make(chan *civ1.ConnectResponse, 1)
	d.Register(runnerID, []string{"linux"}, ch)

	claimed, err := store.ClaimJob(ctx, runnerID, []string{"linux"})
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if claimed.ID != job.ID {
		t.Fatalf("want to claim job %s, got %s", job.ID, claimed.ID)
	}

	if err := d.Dispatch(context.Background(), ci.DispatchJob{JobID: job.ID, RunID: run.ID, RunCmd: job.RunCmd}, []string{"linux"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	<-ch // drain the job message so the send doesn't matter to this test

	// The runner disconnects without ever reporting status.
	d.Unregister(context.Background(), runnerID)

	got, err := store.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != "failure" {
		t.Fatalf("want status failure after runner disconnected, got %q", got.Status)
	}
	if !strings.Contains(got.Detail, "runner disconnected") {
		t.Fatalf("want detail containing %q, got %q", "runner disconnected", got.Detail)
	}
}
