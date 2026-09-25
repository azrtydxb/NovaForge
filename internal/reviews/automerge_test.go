package reviews_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/reviews"
)

// spyGateChecker wraps a stubGateChecker, recording whether MayMerge was
// ever called — how TestAutoMergeUsesTheSameMergePath proves Consider
// actually reaches the gate controller rather than taking a privileged
// shortcut around it.
type spyGateChecker struct {
	inner  stubGateChecker
	called *bool
}

func (s spyGateChecker) MayMerge(ctx context.Context, runID uuid.UUID) (bool, []string, error) {
	*s.called = true
	return s.inner.MayMerge(ctx, runID)
}

func newAutoMergeRun(t *testing.T, store *reviews.Store, ctx context.Context, orgID uuid.UUID, sourceRef string) reviews.Run {
	t.Helper()
	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: sourceRef, TargetRef: "main",
		AuthorID: uuid.New(), AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func TestAutoMergeRefusedWhenGateFails(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newAutoMergeRun(t, store, ctx, orgID, "src")
	if err := store.SubmitReviewAt(ctx, run.ID, uuid.New(), "user", "approve", reviewedSHA, "reviewed this revision"); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	git := &stubMergeGitClient{}
	m := &reviews.Merger{Store: store, Gates: stubGateChecker{allowed: false, reasons: []string{`gate "tests" status "fail"`}}, Git: git}
	a := &reviews.AutoMerger{
		Store:  store,
		Merger: m,
		Policy: reviews.AutoMergePolicy{Enabled: true},
		Impact: func(context.Context, reviews.Run) (reviews.Impact, error) {
			return reviews.Impact{FilesChanged: 1, Paths: []string{"internal/foo/bar.go"}}, nil
		},
	}

	merged, reason, err := a.Consider(ctx, run.ID)
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if merged {
		t.Fatal("want merged false when a required gate fails")
	}
	if !strings.Contains(reason, "tests") {
		t.Fatalf("want reason naming the failing gate, got %q", reason)
	}
	if git.called {
		t.Fatal("want git merge RPC never called when a gate fails")
	}
}

func TestAutoMergeRefusedForForbiddenPath(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newAutoMergeRun(t, store, ctx, orgID, "src")

	git := &stubMergeGitClient{mergeSHA: "deadbeef"}
	m := &reviews.Merger{Store: store, Gates: stubGateChecker{allowed: true}, Git: git}
	a := &reviews.AutoMerger{
		Store:  store,
		Merger: m,
		Policy: reviews.AutoMergePolicy{Enabled: true, ForbiddenPaths: []string{"internal/auth/"}},
		Impact: func(context.Context, reviews.Run) (reviews.Impact, error) {
			return reviews.Impact{FilesChanged: 1, Paths: []string{"internal/auth/session.go"}}, nil
		},
	}

	merged, reason, err := a.Consider(ctx, run.ID)
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if merged {
		t.Fatal("want merged false for a change touching a forbidden path")
	}
	if !strings.Contains(reason, "internal/auth/") {
		t.Fatalf("want reason naming the forbidden path, got %q", reason)
	}
	if git.called {
		t.Fatal("want git merge RPC never called for a forbidden-path change, even with gates green")
	}
}

func TestAutoMergeRefusedAboveFileCap(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newAutoMergeRun(t, store, ctx, orgID, "src")

	paths := make([]string, 40)
	for i := range paths {
		paths[i] = "file.go"
	}

	git := &stubMergeGitClient{mergeSHA: "deadbeef"}
	m := &reviews.Merger{Store: store, Gates: stubGateChecker{allowed: true}, Git: git}
	a := &reviews.AutoMerger{
		Store:  store,
		Merger: m,
		Policy: reviews.AutoMergePolicy{Enabled: true, MaxFilesChanged: 20},
		Impact: func(context.Context, reviews.Run) (reviews.Impact, error) {
			return reviews.Impact{FilesChanged: 40, Paths: paths}, nil
		},
	}

	merged, reason, err := a.Consider(ctx, run.ID)
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if merged {
		t.Fatal("want merged false for a 40-file change under a 20-file cap")
	}
	if !strings.Contains(reason, "40") || !strings.Contains(reason, "20") {
		t.Fatalf("want reason naming both the change size and the cap, got %q", reason)
	}
	if git.called {
		t.Fatal("want git merge RPC never called above the file cap")
	}
}

func TestAutoMergeRefusedWhenDisabled(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newAutoMergeRun(t, store, ctx, orgID, "src")
	if err := store.SubmitReviewAt(ctx, run.ID, uuid.New(), "user", "approve", reviewedSHA, "reviewed this revision"); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	git := &stubMergeGitClient{mergeSHA: "deadbeef"}
	m := &reviews.Merger{Store: store, Gates: stubGateChecker{allowed: true}, Git: git}
	a := &reviews.AutoMerger{
		Store:  store,
		Merger: m,
		Policy: reviews.AutoMergePolicy{Enabled: false},
		Impact: func(context.Context, reviews.Run) (reviews.Impact, error) {
			return reviews.Impact{FilesChanged: 1}, nil
		},
	}

	merged, reason, err := a.Consider(ctx, run.ID)
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if merged {
		t.Fatal("want merged false when the policy is disabled, even though everything else would allow it")
	}
	if reason == "" {
		t.Fatal("want a non-empty reason when disabled")
	}
	if git.called {
		t.Fatal("want git merge RPC never called when auto-merge is disabled")
	}
}

func TestAutoMergeUsesTheSameMergePath(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newAutoMergeRun(t, store, ctx, orgID, "src")
	if err := store.SubmitReviewAt(ctx, run.ID, uuid.New(), "user", "approve", reviewedSHA, "reviewed this revision"); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	git := &stubMergeGitClient{mergeSHA: "deadbeef"}
	var gateCalled bool
	m := &reviews.Merger{
		Store: store,
		Gates: spyGateChecker{inner: stubGateChecker{allowed: true}, called: &gateCalled},
		Git:   git,
	}
	a := &reviews.AutoMerger{
		Store:  store,
		Merger: m,
		Policy: reviews.AutoMergePolicy{Enabled: true},
		Impact: func(context.Context, reviews.Run) (reviews.Impact, error) {
			return reviews.Impact{FilesChanged: 1, Paths: []string{"internal/foo/bar.go"}}, nil
		},
	}

	merged, reason, err := a.Consider(ctx, run.ID)
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if !merged {
		t.Fatalf("want merged true when policy allows and gates/approval are satisfied, reason=%q", reason)
	}
	if !gateCalled {
		t.Fatal("want Consider to reach the gate controller's MayMerge through Merger.Merge — no privileged path around it")
	}
	if !git.called {
		t.Fatal("want the git merge RPC actually called")
	}
}
