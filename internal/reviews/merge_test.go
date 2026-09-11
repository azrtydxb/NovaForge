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
	if err := store.SubmitReview(ctx, run.ID, reviewerID, "user", "approve"); err != nil {
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
