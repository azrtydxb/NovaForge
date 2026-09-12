package reviews_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/reviews"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func storePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "reviews", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newStore(t *testing.T) *reviews.Store {
	t.Helper()
	return reviews.NewStore(storePool(t))
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}

func TestCreateRunAllocatesNumber(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	authorID := uuid.New()
	ctx := scopedCtx(orgID)

	run1, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: repoID, Title: "first", SourceRef: "a", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun 1: %v", err)
	}
	run2, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: repoID, Title: "second", SourceRef: "b", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun 2: %v", err)
	}

	if run1.Number != 1 {
		t.Fatalf("want number 1, got %d", run1.Number)
	}
	if run2.Number != 2 {
		t.Fatalf("want number 2, got %d", run2.Number)
	}
}

func TestProofRecordsAccumulate(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	authorID := uuid.New()
	ctx := scopedCtx(orgID)

	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: repoID, Title: "proofed", SourceRef: "a", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := store.RecordProof(ctx, run.ID, "tests", "passed", "all green"); err != nil {
		t.Fatalf("RecordProof tests: %v", err)
	}
	if err := store.RecordProof(ctx, run.ID, "security", "passed", "no findings"); err != nil {
		t.Fatalf("RecordProof security: %v", err)
	}

	proofs, err := store.ListProof(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListProof: %v", err)
	}
	if len(proofs) != 2 {
		t.Fatalf("want 2 proof records, got %d", len(proofs))
	}
	byGate := map[string]reviews.ProofRecord{}
	for _, p := range proofs {
		byGate[p.Gate] = p
	}
	if byGate["tests"].Status != "passed" {
		t.Fatalf("want tests status passed, got %q", byGate["tests"].Status)
	}
	if byGate["security"].Status != "passed" {
		t.Fatalf("want security status passed, got %q", byGate["security"].Status)
	}
}

func TestAuthorCannotBeSoleApprover(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	agentA := uuid.New()
	ctx := scopedCtx(orgID)

	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: repoID, Title: "self-approve", SourceRef: "a", TargetRef: "main",
		AuthorID: agentA, AuthorKind: "agent",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	err = store.SubmitReview(ctx, run.ID, agentA, "agent", "approve")
	if err == nil {
		t.Fatal("want error for self-approval")
	}
	if !strings.Contains(err.Error(), "author cannot approve") {
		t.Fatalf("want error containing %q, got %q", "author cannot approve", err.Error())
	}
}

func TestSecondReviewerApproves(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	agentA := uuid.New()
	reviewerB := uuid.New()
	ctx := scopedCtx(orgID)

	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: repoID, Title: "reviewed", SourceRef: "a", TargetRef: "main",
		AuthorID: agentA, AuthorKind: "agent",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := store.SubmitReview(ctx, run.ID, reviewerB, "user", "approve"); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
}

// TestGetRunByNumber pins the addressing people and tools actually use —
// "run #2 on this repository" — and that it stays inside the caller's
// organization.
func TestGetRunByNumber(t *testing.T) {
	store := newStore(t)
	orgA, orgB := uuid.New(), uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgA)

	created, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgA, RepoID: repoID, Title: "by number", SourceRef: "a", TargetRef: "main",
		AuthorID: uuid.New(), AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := store.GetRunByNumber(ctx, repoID, created.Number)
	if err != nil {
		t.Fatalf("GetRunByNumber: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("want run %s, got %s", created.ID, got.ID)
	}

	if _, err := store.GetRunByNumber(scopedCtx(orgB), repoID, created.Number); err == nil {
		t.Fatal("another organization resolved this repository's run by number")
	}
}
