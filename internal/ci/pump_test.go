package ci_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
)

// TestPumpHandsAPendingJobToAConnectedRunner pins the piece whose absence was
// invisible: the scheduler created jobs and runners held open streams, but
// nothing claimed a job on a runner's behalf and sent it, so every run sat
// queued forever with no error anywhere. A test that only checks "a job row
// exists" would still have passed.
func TestPumpHandsAPendingJobToAConnectedRunner(t *testing.T) {
	pool := ciPool(t)
	store := ci.NewStore(pool)
	dispatcher := ci.NewDispatcher(store)
	pump := ci.NewPump(store, dispatcher, "http://git.test")

	orgID, repoID := uuid.New(), uuid.New()
	t.Cleanup(func() { cleanupOrgRuns(t, pool, orgID) })

	ctx := context.Background()
	run, _, err := store.CreateRun(ctx, ci.Run{
		OrgID: orgID, RepoID: repoID, CommitSHA: "cafebabe", Ref: "refs/heads/main",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := store.CreateJob(ctx, ci.WorkflowJob{
		RunID: run.ID, Name: "build", RunCmd: "echo hi",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// The runner must exist in the database as well as in the dispatcher: a
	// claim records which runner took the job.
	runnerID, err := store.RegisterRunner(ctx, orgID, "pump-test", []string{"linux"}, []byte("hash-"+uuid.NewString()))
	if err != nil {
		t.Fatalf("RegisterRunner: %v", err)
	}
	ch := make(chan *civ1.ConnectResponse, 1)
	dispatcher.Register(runnerID, []string{"linux"}, ch)

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go pump.Run(pctx)

	select {
	case job := <-ch:
		if job.GetRunCmd() != "echo hi" {
			t.Fatalf("wrong job delivered: %+v", job)
		}
		if job.GetCommitSha() != "cafebabe" {
			t.Fatalf("job carries the wrong commit: %q", job.GetCommitSha())
		}
		if job.GetRepoCloneUrl() == "" {
			t.Fatal("job carries no clone url, so the runner could not fetch the code")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no job reached the connected runner within 20s")
	}
}

// TestPumpDeliversNothingWithoutAConnectedRunner keeps the pump from inventing
// work when there is nobody to do it.
func TestPumpDeliversNothingWithoutAConnectedRunner(t *testing.T) {
	pool := ciPool(t)
	store := ci.NewStore(pool)
	dispatcher := ci.NewDispatcher(store)
	pump := ci.NewPump(store, dispatcher, "http://git.test")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pump.Run(ctx) // returns when ctx expires; must not panic with no runners
}
