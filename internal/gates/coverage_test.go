package gates_test

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestInvalidCoverageThresholdCannotPass(t *testing.T) {
	dir := module(t, map[string]string{
		"calc.go":      "package gate\nfunc A() int { return 1 }\n",
		"calc_test.go": "package gate\nimport \"testing\"\nfunc TestA(t *testing.T) { A() }\n",
	})
	for _, minimum := range []any{math.NaN(), math.Inf(1), -1, 101, "eighty"} {
		eval, err := gates.Runners["tests"](context.Background(), input(dir, map[string]any{"minimum_coverage": minimum}))
		if err != nil || eval.Status != "error" {
			t.Fatalf("invalid coverage threshold %v was not rejected: %+v %v", minimum, eval, err)
		}
	}
}

func TestCoverageHistoryPreservesScopeAndAbsence(t *testing.T) {
	pool := storePool(t)
	store := gates.NewStore(pool)
	org, repo, run := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM gates.gate_evaluations WHERE org_id=$1", org)
	})
	ctx := scopedCtx(org)
	value := 100.0
	e := gates.Evaluation{OrgID: org, RepoID: repo, RunID: run, Gate: "tests", Status: "pass", TargetSHA: "first", CoveragePercent: &value}
	if err := store.RecordEvaluation(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.TargetSHA, value = "second", 0
	if err := store.RecordEvaluation(ctx, e); err != nil {
		t.Fatal(err)
	}
	// A cached SHA remains one row, not an extra historical sample.
	if err := store.RecordEvaluation(ctx, e); err != nil {
		t.Fatal(err)
	}
	server := gates.NewGRPCServer(&gates.Controller{Store: store}, nil, nil, nil)
	req := &gatesv1.ListCoverageHistoryRequest{RepoId: repo.String()}
	history, err := server.ListCoverageHistory(ctx, req)
	if err != nil || len(history.GetEvaluations()) != 2 {
		t.Fatalf("history: %v %v", history, err)
	}
	latest, previous := history.Evaluations[0], history.Evaluations[1]
	if latest.CoveragePercent == nil || latest.GetCoveragePercent() != 0 || previous.GetCoveragePercent() != 100 || latest.GetRepoId() != repo.String() {
		t.Fatalf("lost measured zero, identity or history: %v", history)
	}
	foreign := uuid.New()
	other, err := server.ListCoverageHistory(scopedCtx(foreign), req)
	if err != nil || len(other.GetEvaluations()) != 0 {
		t.Fatalf("cross-org history: %v %v", other, err)
	}
	e.OrgID = foreign
	if err := store.RecordEvaluation(scopedCtx(foreign), e); err == nil {
		t.Fatal("cross-org upsert overwrote coverage")
	}
	e.OrgID, e.TargetSHA, e.Status, e.CoveragePercent = org, "third", "error", nil
	if err := store.RecordEvaluation(ctx, e); err != nil {
		t.Fatal(err)
	}
	history, err = server.ListCoverageHistory(ctx, req)
	if err != nil || len(history.GetEvaluations()) != 2 || history.Evaluations[0].CoveragePercent != nil || history.Evaluations[0].GetTargetSha() != "third" {
		t.Fatalf("unavailable latest evidence was skipped: %v %v", history, err)
	}
	for _, invalid := range []float64{math.NaN(), math.Inf(1), -1, 101} {
		e.Status, e.CoveragePercent = "pass", &invalid
		if err := store.RecordEvaluation(ctx, e); err == nil {
			t.Fatalf("invalid coverage accepted: %v", invalid)
		}
	}
}
