package gates_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
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
	if err := database.Migrate(url, "gates", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newStore(t *testing.T) *gates.Store {
	t.Helper()
	return gates.NewStore(storePool(t))
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "agent",
	})
}

func TestRecordEvaluationUpserts(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	runID := uuid.New()

	err := store.RecordEvaluation(ctx, gates.Evaluation{
		OrgID:     orgID,
		RunID:     runID,
		Gate:      "tests",
		Status:    "fail",
		Detail:    "2 tests failed",
		TargetSHA: "aaa",
	})
	if err != nil {
		t.Fatalf("RecordEvaluation (fail): %v", err)
	}

	err = store.RecordEvaluation(ctx, gates.Evaluation{
		OrgID:     orgID,
		RunID:     runID,
		Gate:      "tests",
		Status:    "pass",
		Detail:    "all tests passed",
		TargetSHA: "aaa",
	})
	if err != nil {
		t.Fatalf("RecordEvaluation (pass): %v", err)
	}

	evals, err := store.ListEvaluations(ctx, runID)
	if err != nil {
		t.Fatalf("ListEvaluations: %v", err)
	}
	if len(evals) != 1 {
		t.Fatalf("want exactly 1 evaluation after upsert, got %d", len(evals))
	}
	if evals[0].Status != "pass" {
		t.Fatalf("want status pass, got %q", evals[0].Status)
	}
}

func TestEvaluationsAreShaScoped(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	runID := uuid.New()

	err := store.RecordEvaluation(ctx, gates.Evaluation{
		OrgID:     orgID,
		RunID:     runID,
		Gate:      "tests",
		Status:    "pass",
		TargetSHA: "aaa",
	})
	if err != nil {
		t.Fatalf("RecordEvaluation: %v", err)
	}

	latest, err := store.LatestForSHA(ctx, runID, "bbb")
	if err != nil {
		t.Fatalf("LatestForSHA: %v", err)
	}
	if _, ok := latest["tests"]; ok {
		t.Fatal("want no entry for gate tests at a new SHA, found one")
	}

	latestSameSHA, err := store.LatestForSHA(ctx, runID, "aaa")
	if err != nil {
		t.Fatalf("LatestForSHA: %v", err)
	}
	if _, ok := latestSameSHA["tests"]; !ok {
		t.Fatal("want entry for gate tests at the recorded SHA, found none")
	}
}

func TestListIsOrgScoped(t *testing.T) {
	store := newStore(t)
	orgA := uuid.New()
	orgB := uuid.New()
	runA := uuid.New()
	runB := uuid.New()

	if err := store.RecordEvaluation(scopedCtx(orgA), gates.Evaluation{
		OrgID: orgA, RunID: runA, Gate: "tests", Status: "pass", TargetSHA: "aaa",
	}); err != nil {
		t.Fatalf("RecordEvaluation org A: %v", err)
	}
	if err := store.RecordEvaluation(scopedCtx(orgB), gates.Evaluation{
		OrgID: orgB, RunID: runB, Gate: "tests", Status: "pass", TargetSHA: "aaa",
	}); err != nil {
		t.Fatalf("RecordEvaluation org B: %v", err)
	}

	evalsForRunB, err := store.ListEvaluations(scopedCtx(orgA), runB)
	if err != nil {
		t.Fatalf("ListEvaluations: %v", err)
	}
	if len(evalsForRunB) != 0 {
		t.Fatalf("want org A to see no evaluations for org B's run, got %d", len(evalsForRunB))
	}
}
