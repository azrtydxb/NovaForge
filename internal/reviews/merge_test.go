package reviews_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/reviews"
)

// stubGateChecker lets tests control what the gate controller reports
// without standing up a real gates service.
type stubGateChecker struct {
	allowed bool
	reasons []string
	err     error
}

func (s stubGateChecker) MayMerge(context.Context, uuid.UUID) (bool, []string, error) {
	return s.allowed, s.reasons, s.err
}

// stubGitClient implements gitv1.GitServiceClient by embedding the nil
// interface and overriding only Merge, tracking whether it was called.
type stubMergeGitClient struct {
	gitv1.GitServiceClient
	called   bool
	mergeSHA string
	err      error
}

func (s *stubMergeGitClient) Merge(_ context.Context, _ *gitv1.MergeRequest, _ ...grpc.CallOption) (*gitv1.MergeResponse, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	return &gitv1.MergeResponse{MergeSha: s.mergeSHA}, nil
}

func (s *stubMergeGitClient) ListCommits(context.Context, *gitv1.ListCommitsRequest, ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	return &gitv1.ListCommitsResponse{Commits: []*gitv1.Commit{{Sha: reviewedSHA}}}, nil
}
func (s *stubMergeGitClient) GetDiff(context.Context, *gitv1.GetDiffRequest, ...grpc.CallOption) (*gitv1.GetDiffResponse, error) {
	return &gitv1.GetDiffResponse{Unified: "diff --git a/example b/example\n+review this line\n"}, nil
}

func TestMergeBlockedByFailingGate(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorID := uuid.New()
	ctx := scopedCtx(orgID)
	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	git := &stubMergeGitClient{}
	m := &reviews.Merger{
		Store: store,
		Gates: stubGateChecker{allowed: false, reasons: []string{"gate \"tests\" status \"fail\""}},
		Git:   git,
	}

	_, err = m.Merge(ctx, run.ID, "merge")
	if err == nil || !errors.Is(err, reviews.ErrMergeBlocked) {
		t.Fatalf("want error satisfying errors.Is(err, ErrMergeBlocked), got %v", err)
	}
	if git.called {
		t.Fatal("want git merge RPC never called when the gate controller refuses")
	}
}

func TestMergeBlockedWhenControllerUnreachable(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorID := uuid.New()
	ctx := scopedCtx(orgID)
	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	git := &stubMergeGitClient{}
	m := &reviews.Merger{
		Store: store,
		Gates: stubGateChecker{err: status.Error(codes.Unavailable, "gates service unreachable")},
		Git:   git,
	}

	_, err = m.Merge(ctx, run.ID, "merge")
	if err == nil {
		t.Fatal("want error when the gate controller is unreachable")
	}
	if !errors.Is(err, reviews.ErrMergeBlocked) {
		t.Fatalf("want error satisfying errors.Is(err, ErrMergeBlocked), got %v", err)
	}
	if git.called {
		t.Fatal("want git merge RPC never called when the gate controller is unreachable: fail closed, not open")
	}
}

func TestMergeProceedsWhenAllowed(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorID := uuid.New()
	reviewerID := uuid.New()
	ctx := scopedCtx(orgID)
	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := store.SubmitReviewAt(ctx, run.ID, reviewerID, "user", "approve", reviewedSHA, "reviewed this revision"); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	git := &stubMergeGitClient{mergeSHA: "deadbeef"}
	m := &reviews.Merger{
		Store: store,
		Gates: stubGateChecker{allowed: true},
		Git:   git,
	}

	sha, err := m.Merge(ctx, run.ID, "merge")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if sha != "deadbeef" {
		t.Fatalf("want merge sha 'deadbeef', got %q", sha)
	}
	if !git.called {
		t.Fatal("want git merge RPC called when allowed")
	}

	got, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != "merged" {
		t.Fatalf("want run state 'merged', got %q", got.State)
	}
}

func TestMergeRequiresIndependentApproval(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorID := uuid.New()
	ctx := scopedCtx(orgID)
	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// The author attempting to approve their own run is rejected by the
	// store itself, so no independent approval exists at all.
	if err := store.SubmitReview(ctx, run.ID, authorID, "user", "approve"); err == nil {
		t.Fatal("want self-approval to be rejected by the store")
	}

	git := &stubMergeGitClient{mergeSHA: "deadbeef"}
	m := &reviews.Merger{
		Store: store,
		Gates: stubGateChecker{allowed: true},
		Git:   git,
	}

	_, err = m.Merge(ctx, run.ID, "merge")
	if err == nil {
		t.Fatal("want merge refused: only the author's own (rejected) approval exists")
	}
	if git.called {
		t.Fatal("want git merge RPC never called without an independent approval")
	}
}

// evaluatingGateChecker records the order of Evaluate and MayMerge calls.
type evaluatingGateChecker struct {
	calls   *[]string
	evalErr error
}

func (e evaluatingGateChecker) Evaluate(context.Context, uuid.UUID) error {
	*e.calls = append(*e.calls, "evaluate")
	return e.evalErr
}

func (e evaluatingGateChecker) MayMerge(context.Context, uuid.UUID) (bool, []string, error) {
	*e.calls = append(*e.calls, "may-merge")
	return false, []string{"not yet"}, nil
}

// TestMergeEvaluatesGatesFirst pins that a merge runs the run's gates before
// asking whether it may merge. Nothing else ever called Evaluate, so every
// required gate stayed "not evaluated at head" and no repository that declared
// a gate could merge. An evaluation that fails blocks the merge.
func TestMergeEvaluatesGatesFirst(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: uuid.New(), AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	var calls []string
	git := &stubMergeGitClient{}
	m := &reviews.Merger{Store: store, Gates: evaluatingGateChecker{calls: &calls}, Git: git}
	if _, err := m.Merge(ctx, run.ID, "merge"); !errors.Is(err, reviews.ErrMergeBlocked) {
		t.Fatalf("Merge: %v, want blocked", err)
	}
	if len(calls) != 2 || calls[0] != "evaluate" || calls[1] != "may-merge" {
		t.Fatalf("calls = %v, want evaluate then may-merge", calls)
	}

	calls = nil
	m.Gates = evaluatingGateChecker{calls: &calls, evalErr: errors.New("tests gate: go not installed")}
	if _, err := m.Merge(ctx, run.ID, "merge"); !errors.Is(err, reviews.ErrMergeBlocked) {
		t.Fatalf("Merge with a failed evaluation: %v, want blocked", err)
	}
	if len(calls) != 1 || git.called {
		t.Fatalf("calls = %v, git called = %v; a failed evaluation must stop the merge", calls, git.called)
	}
}
