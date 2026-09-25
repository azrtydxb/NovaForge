package gates_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestLegacyWriterCannotRetagQualifiedEvaluation(t *testing.T) {
	pool := storePool(t)
	store := gates.NewStore(pool)
	org, run, repo := uuid.New(), uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	if err := store.RecordEvaluation(ctx, gates.Evaluation{OrgID: org, RunID: run, RepoID: repo, Gate: "tests", Status: "fail", TargetSHA: "source", PolicySHA: "strict-policy"}); err != nil {
		t.Fatal(err)
	}
	// Exact pre-policy upsert shape: a replica finishing old work knows nothing
	// about policy_sha and therefore leaves a newer row's tag untouched.
	_, legacyErr := pool.Exec(ctx, `INSERT INTO gates.gate_evaluations
		(id,org_id,run_id,gate,status,detail,target_sha,evaluated_at,repo_id,coverage_percent)
		VALUES($1,$2,$3,'tests','pass','old permissive policy','source',now(),$4,NULL)
		ON CONFLICT(run_id,gate,target_sha) DO UPDATE SET status=EXCLUDED.status,
		detail=EXCLUDED.detail,repo_id=EXCLUDED.repo_id,coverage_percent=EXCLUDED.coverage_percent,evaluated_at=now()
		WHERE gates.gate_evaluations.org_id=EXCLUDED.org_id
		AND (gates.gate_evaluations.repo_id=EXCLUDED.repo_id OR gates.gate_evaluations.repo_id='00000000-0000-0000-0000-000000000000')`, uuid.New(), org, run, repo)
	latest, err := store.LatestForSHA(ctx, run, "source", "strict-policy")
	if err != nil {
		t.Fatal(err)
	}
	if latest["tests"].Status != "fail" {
		t.Fatalf("legacy writer replaced strict-policy failure with %+v (legacy error: %v)", latest["tests"], legacyErr)
	}
	var pgErr *pgconn.PgError
	if !errors.As(legacyErr, &pgErr) || pgErr.Code != "42P10" {
		t.Fatalf("old conflict identity must be refused by PostgreSQL: %v", legacyErr)
	}
}

func TestPolicyRevisionsHaveSeparateEvaluationIdentity(t *testing.T) {
	store := newStore(t)
	org, run, repo := uuid.New(), uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	for _, policy := range []string{"strict-policy", "old-policy"} {
		status := "fail"
		if policy == "old-policy" {
			status = "pass"
		}
		if err := store.RecordEvaluation(ctx, gates.Evaluation{OrgID: org, RunID: run, RepoID: repo, Gate: "tests", Status: status, TargetSHA: "source", PolicySHA: policy}); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := store.LatestForSHA(ctx, run, "source", "strict-policy")
	if err != nil || latest["tests"].Status != "fail" {
		t.Fatalf("later old-policy result erased strict-policy evidence: %+v (%v)", latest, err)
	}
	legacy, err := store.LatestForSHA(ctx, run, "source")
	if err != nil || len(legacy) != 0 {
		t.Fatalf("unqualified lookup borrowed policy-tagged evidence: %+v (%v)", legacy, err)
	}
}

func TestPolicyFenceDowngradeRefusesQualifiedEvidence(t *testing.T) {
	pool := storePool(t)
	store := gates.NewStore(pool)
	org, run := uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	if err := store.RecordEvaluation(ctx, gates.Evaluation{OrgID: org, RunID: run, Gate: "tests", Status: "fail", TargetSHA: "source", PolicySHA: "strict-policy"}); err != nil {
		t.Fatal(err)
	}
	migration, err := fs.ReadFile(gates.MigrationsFS, "000004_policy_writer_fence.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	_, downgradeErr := conn.Exec(ctx, string(migration))
	// A refused explicit migration transaction remains aborted on its session;
	// release that transaction before checking evidence via the owner store.
	if _, err := conn.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if downgradeErr == nil || !strings.Contains(downgradeErr.Error(), "cannot downgrade while policy-qualified gate evidence exists") {
		t.Fatalf("qualified evidence did not fence downgrade: %v", downgradeErr)
	}
	latest, err := store.LatestForSHA(ctx, run, "source", "strict-policy")
	if err != nil || latest["tests"].Status != "fail" {
		t.Fatalf("refused downgrade changed qualified evidence: %+v (%v)", latest, err)
	}
}
