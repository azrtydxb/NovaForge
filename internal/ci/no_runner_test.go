package ci_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// TestPendingJobSaysWhenNoRunnerIsConnected pins that a command job which can
// never start says why. With no runner connected for its organization a job is
// accepted and stays pending indefinitely — exactly what a healthy queue that
// is merely busy looks like, so nobody can tell the difference. A deployment
// without runners (OKE runs none: its CNI enforces no network policy, so
// repository code must not run there) made this permanent.
//
// The note is computed when the run is read, not stored when it was queued: a
// runner that connects later must make it disappear without anyone rewriting
// the job.
func TestPendingJobSaysWhenNoRunnerIsConnected(t *testing.T) {
	git := &triggerGitClient{
		stubGitClient: stubGitClient{content: []byte(twoJobWorkflow)},
		name:          "demo", sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	q, store := newTriggerQueryServer(t, git)
	orgID, repoID := uuid.New(), uuid.New()
	cleanupOrgRuns(t, ciPool(t), orgID)
	ctx := triggerCtx(orgID)

	started, err := q.TriggerRun(ctx, &civ1.TriggerRunRequest{RepoId: repoID.String()})
	if err != nil {
		t.Fatalf("TriggerRun: %v", err)
	}
	read := func() *civ1.GetRunResponse {
		t.Helper()
		r, err := q.GetRun(ctx, &civ1.GetRunRequest{Id: started.GetRun().GetId()})
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		return r
	}

	before := read()
	if len(before.GetJobs()) == 0 {
		t.Fatal("the run has no jobs")
	}
	for _, j := range before.GetJobs() {
		if j.GetStatus() != "pending" {
			continue
		}
		if !strings.Contains(j.GetDetail(), "no CI runner is connected") {
			t.Fatalf("pending job %q with no runner in the organization says %q; it must say why it cannot start", j.GetName(), j.GetDetail())
		}
	}

	// A runner connecting for this organization is the only thing that makes
	// the job startable, and the note must go with it.
	runner, err := store.RegisterRunner(ctx, orgID, "r1", nil, []byte(uuid.NewString()))
	if err != nil {
		t.Fatalf("RegisterRunner: %v", err)
	}
	conn, err := store.BeginRunnerConnection(ctx, runner)
	if err != nil {
		t.Fatalf("BeginRunnerConnection: %v", err)
	}
	for _, j := range read().GetJobs() {
		if strings.Contains(j.GetDetail(), "no CI runner is connected") {
			t.Fatalf("job %q still says no runner is connected after one connected: %q", j.GetName(), j.GetDetail())
		}
	}

	// Another organization's runner does not make this one's jobs startable.
	other := uuid.New()
	cleanupOrgRuns(t, ciPool(t), other)
	otherRunner, err := store.RegisterRunner(triggerCtx(other), other, "r2", nil, []byte(uuid.NewString()))
	if err != nil {
		t.Fatalf("RegisterRunner other: %v", err)
	}
	if _, err := store.BeginRunnerConnection(triggerCtx(other), otherRunner); err != nil {
		t.Fatalf("BeginRunnerConnection other: %v", err)
	}
	if err := store.EndRunnerConnection(ctx, runner, conn); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	for _, j := range read().GetJobs() {
		if j.GetStatus() == "pending" && !strings.Contains(j.GetDetail(), "no CI runner is connected") {
			t.Fatalf("another organization's connected runner hid this organization's missing one: %q", j.GetDetail())
		}
	}
}
