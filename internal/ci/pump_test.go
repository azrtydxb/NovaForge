package ci_test

import (
	"context"
	"strings"
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
	pump := ci.NewPump(store, dispatcher, "http://git.test", "test-secret")

	orgID, repoID := uuid.New(), uuid.New()
	t.Cleanup(func() { cleanupOrgRuns(t, pool, orgID) })

	ctx := context.Background()
	run, _, err := store.CreateRun(ctx, ci.Run{
		OrgID: orgID, RepoID: repoID, RepoName: "widgets",
		CommitSHA: "cafebabe", Ref: "refs/heads/main",
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
		// The clone URL must carry a credential: a CI job has no person behind
		// it and cannot borrow anyone's, and without one the clone fails with
		// git's generic exit 128 and no hint that auth was the problem.
		if !strings.Contains(job.GetRepoCloneUrl(), "@") {
			t.Fatalf("clone url carries no credential: %q", job.GetRepoCloneUrl())
		}
		if !strings.Contains(job.GetRepoCloneUrl(), "nfsvc.") {
			t.Fatalf("clone url does not carry a service token: %q", job.GetRepoCloneUrl())
		}
		// The URL must address the repository by NAME: the on-disk path and the
		// smart-HTTP route are keyed by name, and an id there fails the clone
		// with git's opaque exit 128.
		if !strings.HasSuffix(job.GetRepoCloneUrl(), "/widgets.git") {
			t.Fatalf("clone url does not address the repository by name: %q", job.GetRepoCloneUrl())
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
	pump := ci.NewPump(store, dispatcher, "http://git.test", "test-secret")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pump.Run(ctx) // returns when ctx expires; must not panic with no runners
}

// TestClaimNeverCrossesOrganizations pins a boundary the dispatch path had
// silently left open: the claim picked any pending job, so a runner registered
// to one organization could be handed another organization's job — and its
// source, along with whatever secrets that job carries. Organizations are a
// hard boundary; this is where a background worker could have softened it.
func TestClaimNeverCrossesOrganizations(t *testing.T) {
	pool := ciPool(t)
	store := ci.NewStore(pool)
	ctx := context.Background()

	orgA, orgB := uuid.New(), uuid.New()
	t.Cleanup(func() { cleanupOrgRuns(t, pool, orgA); cleanupOrgRuns(t, pool, orgB) })

	// A pending job belonging to org A.
	runA, _, err := store.CreateRun(ctx, ci.Run{
		OrgID: orgA, RepoID: uuid.New(), RepoName: "a-repo",
		CommitSHA: "aaaa", Ref: "refs/heads/main",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := store.CreateJob(ctx, ci.WorkflowJob{
		RunID: runA.ID, Name: "build", RunCmd: "echo a",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// A runner belonging to org B must not be able to claim it.
	runnerB, err := store.RegisterRunner(ctx, orgB, "b-runner", []string{"linux"},
		[]byte("hash-"+uuid.NewString()))
	if err != nil {
		t.Fatalf("RegisterRunner: %v", err)
	}
	if _, _, err := store.ClaimForDispatch(ctx, runnerB, []string{"linux"}); err == nil {
		t.Fatal("a runner claimed a job from another organization")
	}

	// A runner in org A can.
	runnerA, err := store.RegisterRunner(ctx, orgA, "a-runner", []string{"linux"},
		[]byte("hash-"+uuid.NewString()))
	if err != nil {
		t.Fatalf("RegisterRunner: %v", err)
	}
	job, gotOrg, err := store.ClaimForDispatch(ctx, runnerA, []string{"linux"})
	if err != nil {
		t.Fatalf("the owning organization's runner must be able to claim: %v", err)
	}
	if gotOrg != orgA {
		t.Fatalf("claim returned the wrong organization: %s", gotOrg)
	}
	if job.RunCmd != "echo a" {
		t.Fatalf("wrong job claimed: %+v", job)
	}
}
