package reviews_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/reviews"
)

func newExceptionRun(t *testing.T, store *reviews.Store, orgID uuid.UUID, authorKind string) reviews.Run {
	t.Helper()
	ctx := scopedCtx(orgID)
	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "exception fixture", SourceRef: "src", TargetRef: "main",
		AuthorID: uuid.New(), AuthorKind: authorKind,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func TestSummaryCountsEachCategoryOnce(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	// agent_running: an agent-authored run with no proof recorded yet.
	newExceptionRun(t, store, orgID, "agent")

	// ready_to_automerge (healthy): a passing gate plus an independent approval.
	healthy := newExceptionRun(t, store, orgID, "user")
	if err := store.RecordProof(proofServiceContext(t, ctx, "gates"), healthy.ID, "tests", "pass", "all tests passed"); err != nil {
		t.Fatalf("RecordProof healthy: %v", err)
	}
	if err := store.SubmitReviewAt(ctx, healthy.ID, uuid.New(), "user", "approve", reviewedSHA, ""); err != nil {
		t.Fatalf("SubmitReview healthy: %v", err)
	}

	// need_human_review: a human-authored run with nothing recorded yet.
	newExceptionRun(t, store, orgID, "user")

	// architecture_decision: a pending architecture-decision marker.
	arch := newExceptionRun(t, store, orgID, "user")
	if err := store.RecordProof(proofServiceContext(t, ctx, "gates"), arch.ID, "architecture-decision", "pending", "needs an ADR for the new service boundary"); err != nil {
		t.Fatalf("RecordProof arch: %v", err)
	}

	// gate_failure: a failing tests gate.
	failing := newExceptionRun(t, store, orgID, "user")
	if err := store.RecordProof(proofServiceContext(t, ctx, "gates"), failing.ID, "tests", "fail", "2 tests failed"); err != nil {
		t.Fatalf("RecordProof failing: %v", err)
	}

	// agent_blocked: an agent-authored run whose agent-blocked marker failed.
	blocked := newExceptionRun(t, store, orgID, "agent")
	if err := store.RecordProof(proofServiceContext(t, ctx, "agent-runtime"), blocked.ID, "agent-blocked", "fail", "wall-clock budget exceeded"); err != nil {
		t.Fatalf("RecordProof blocked: %v", err)
	}

	summary, _, err := store.Exceptions(ctx, orgID, &revisionGit{head: reviewedSHA})
	if err != nil {
		t.Fatalf("Exceptions: %v", err)
	}
	if summary.AgentsRunning != 1 {
		t.Errorf("want AgentsRunning 1, got %d", summary.AgentsRunning)
	}
	if summary.ReadyToAutoMerge != 1 {
		t.Errorf("want ReadyToAutoMerge 1, got %d", summary.ReadyToAutoMerge)
	}
	if summary.NeedHumanReview != 1 {
		t.Errorf("want NeedHumanReview 1, got %d", summary.NeedHumanReview)
	}
	if summary.ArchitectureDecisions != 1 {
		t.Errorf("want ArchitectureDecisions 1, got %d", summary.ArchitectureDecisions)
	}
	if summary.GateFailures != 1 {
		t.Errorf("want GateFailures 1, got %d", summary.GateFailures)
	}
	if summary.AgentsBlocked != 1 {
		t.Errorf("want AgentsBlocked 1, got %d", summary.AgentsBlocked)
	}
}

func TestRunWithFailedGateIsAnException(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	run := newExceptionRun(t, store, orgID, "user")
	if err := store.RecordProof(proofServiceContext(t, ctx, "gates"), run.ID, "tests", "fail", "TestFoo failed"); err != nil {
		t.Fatalf("RecordProof: %v", err)
	}

	summary, items, err := store.Exceptions(ctx, orgID, &revisionGit{head: reviewedSHA})
	if err != nil {
		t.Fatalf("Exceptions: %v", err)
	}
	if summary.GateFailures != 1 {
		t.Fatalf("want GateFailures 1, got %d", summary.GateFailures)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 exception item, got %d", len(items))
	}
	if !strings.Contains(items[0].Reason, "tests") {
		t.Fatalf("want reason naming the failing gate, got %q", items[0].Reason)
	}
}

func TestHealthyAutoMergeableRunIsNotAnException(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	run := newExceptionRun(t, store, orgID, "user")
	if err := store.RecordProof(proofServiceContext(t, ctx, "gates"), run.ID, "tests", "pass", "all tests passed"); err != nil {
		t.Fatalf("RecordProof: %v", err)
	}
	if err := store.SubmitReviewAt(ctx, run.ID, uuid.New(), "user", "approve", reviewedSHA, ""); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	summary, items, err := store.Exceptions(ctx, orgID, &revisionGit{head: reviewedSHA})
	if err != nil {
		t.Fatalf("Exceptions: %v", err)
	}
	if summary.ReadyToAutoMerge != 1 {
		t.Fatalf("want ReadyToAutoMerge 1, got %d", summary.ReadyToAutoMerge)
	}
	for _, item := range items {
		if item.Key == "run-"+strconv.Itoa(run.Number) {
			t.Fatalf("want the healthy run absent from the exception list, found %+v", item)
		}
	}
	if len(items) != 0 {
		t.Fatalf("want no exception items, got %d: %+v", len(items), items)
	}
}

func TestSummaryIsOrgScoped(t *testing.T) {
	store := newStore(t)
	orgA := uuid.New()
	orgB := uuid.New()
	ctxA := scopedCtx(orgA)
	ctxB := scopedCtx(orgB)

	runA := newExceptionRun(t, store, orgA, "user")
	if err := store.RecordProof(proofServiceContext(t, ctxA, "gates"), runA.ID, "tests", "fail", "org A failure"); err != nil {
		t.Fatalf("RecordProof A: %v", err)
	}
	runB := newExceptionRun(t, store, orgB, "user")
	if err := store.RecordProof(proofServiceContext(t, ctxB, "gates"), runB.ID, "tests", "fail", "org B failure"); err != nil {
		t.Fatalf("RecordProof B: %v", err)
	}

	summary, items, err := store.Exceptions(ctxA, orgA, &revisionGit{head: reviewedSHA})
	if err != nil {
		t.Fatalf("Exceptions: %v", err)
	}
	if summary.GateFailures != 1 {
		t.Fatalf("want GateFailures 1 for org A, got %d", summary.GateFailures)
	}
	for _, item := range items {
		if strings.Contains(item.Reason, "org B") {
			t.Fatalf("want org A's summary to include none of org B's runs, got %+v", item)
		}
	}
}
