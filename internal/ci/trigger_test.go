package ci_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/ci"
)

// triggerGitClient answers the three calls TriggerRun makes: resolve the
// repository, resolve the ref to a commit, and read the workflow.
type triggerGitClient struct {
	stubGitClient
	name string
	sha  string
}

func (s *triggerGitClient) GetRepo(ctx context.Context, in *gitv1.GetRepoRequest, opts ...grpc.CallOption) (*gitv1.GetRepoResponse, error) {
	return &gitv1.GetRepoResponse{Repo: &gitv1.Repo{
		Id: in.GetName(), Name: s.name, DefaultBranch: "main",
	}}, nil
}

func (s *triggerGitClient) ListCommits(ctx context.Context, in *gitv1.ListCommitsRequest, opts ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	if s.sha == "" {
		return &gitv1.ListCommitsResponse{}, nil
	}
	return &gitv1.ListCommitsResponse{Commits: []*gitv1.Commit{{Sha: s.sha}}}, nil
}

func newTriggerQueryServer(t *testing.T, git gitv1.GitServiceClient) (*ci.QueryServer, *ci.Store) {
	t.Helper()
	pool := ciPool(t)
	store := ci.NewStore(pool)
	rdb := ciRedis(t)
	sched := ci.NewScheduler(rdb, store, git, ci.SchedulerConfig{
		HMACSecret: "test-secret",
		Stream:     "stream:test:trigger:" + uuid.NewString(),
		Group:      "ci-engine-test",
	})
	q := ci.NewQueryServer(store, nil, nil, nil)
	q.SetScheduler(sched, git)
	return q, store
}

func triggerCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID: orgID, ActorID: uuid.New(), ActorKind: "user",
	})
}

// TestTriggerRunSchedulesTheSameWayAPushDoes pins that a run asked for on
// demand is built by the same scheduler a push uses, so CI cannot behave
// differently depending on which asked.
func TestTriggerRunSchedulesTheSameWayAPushDoes(t *testing.T) {
	git := &triggerGitClient{
		stubGitClient: stubGitClient{content: []byte(twoJobWorkflow)},
		name:          "demo", sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	q, store := newTriggerQueryServer(t, git)
	orgID, repoID := uuid.New(), uuid.New()
	cleanupOrgRuns(t, ciPool(t), orgID)
	ctx := triggerCtx(orgID)

	resp, err := q.TriggerRun(ctx, &civ1.TriggerRunRequest{RepoId: repoID.String()})
	if err != nil {
		t.Fatalf("TriggerRun: %v", err)
	}
	if resp.GetRun().GetCommitSha() != git.sha {
		t.Fatalf("the run is not pinned to the resolved commit: %q", resp.GetRun().GetCommitSha())
	}

	runID, err := uuid.Parse(resp.GetRun().GetId())
	if err != nil {
		t.Fatalf("parse run id: %v", err)
	}
	jobs, err := store.ListJobsForRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("want the workflow's 2 jobs, got %d", len(jobs))
	}

	// Asking twice for the same commit must not produce a second run.
	again, err := q.TriggerRun(ctx, &civ1.TriggerRunRequest{RepoId: repoID.String()})
	if err != nil {
		t.Fatalf("second TriggerRun: %v", err)
	}
	if again.GetRun().GetId() != resp.GetRun().GetId() {
		t.Fatalf("a second trigger for the same commit created a second run")
	}
}

// TestTriggerRunSaysWhyNothingHappened pins that a person asking for a run
// on a commit with no workflow is told, rather than getting the silence a
// push event correctly gets.
func TestTriggerRunSaysWhyNothingHappened(t *testing.T) {
	git := &triggerGitClient{
		stubGitClient: stubGitClient{missing: true},
		name:          "demo", sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	q, _ := newTriggerQueryServer(t, git)
	ctx := triggerCtx(uuid.New())

	_, err := q.TriggerRun(ctx, &civ1.TriggerRunRequest{RepoId: uuid.NewString()})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("want codes.FailedPrecondition, got %v", err)
	}
}
