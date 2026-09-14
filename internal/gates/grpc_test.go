package gates_test

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
)

// runLookupOrgChecked builds a gates.RunLookup that mirrors what the
// production wiring does: it calls the reviews service with the incoming
// ctx (which, over gRPC, carries the caller's resolved scope) to look up
// the run's org, and reviews itself refuses a cross-org lookup. Here that
// is simulated directly against a fixed org, denying any ctx whose scope
// names a different one — exactly what a real RunLookup backed by
// reviews.GRPCServer.GetRun does.
func runLookupOrgChecked(orgID, repoID uuid.UUID, targetRef, headSHA string) gates.RunLookup {
	return func(ctx context.Context, runID uuid.UUID) (gates.RunHead, error) {
		scope, err := authz.FromContext(ctx)
		if err != nil || scope.OrgID != orgID {
			return gates.RunHead{}, status.Error(codes.PermissionDenied, "run does not belong to this organization")
		}
		return gates.RunHead{OrgID: orgID, RepoID: repoID, TargetRef: targetRef, HeadSHA: headSHA}, nil
	}
}

// TestMayMergeRequiresOrgScope asserts a call with a scope for another
// organization than the run's own is refused with PermissionDenied, rather
// than being answered (correctly or not) as if it were entitled to know.
func TestMayMergeRequiresOrgScope(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	runID := uuid.New()
	store := newStore(t)
	controller := &gates.Controller{
		Store: store,
		Git:   singleGateGit(),
		Runs:  runLookupOrgChecked(orgID, repoID, "main", "aaa"),
	}
	srv := gates.NewGRPCServer(controller, nil, nil, nil)

	foreignOrg := uuid.New()
	_, err := srv.MayMerge(scopedCtx(foreignOrg), &gatesv1.MayMergeRequest{RunId: runID.String()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("MayMerge for foreign org: code = %v, want PermissionDenied", status.Code(err))
	}

	// Sanity: the same call succeeds for the run's actual organization.
	_, err = srv.MayMerge(scopedCtx(orgID), &gatesv1.MayMergeRequest{RunId: runID.String()})
	if err != nil {
		t.Fatalf("MayMerge for the run's own org: %v", err)
	}
}

// TestEvaluateIsIdempotent calls Evaluate twice for an unchanged head and
// asserts the gate runner executed only once — Controller.Evaluate reuses
// an evaluation already recorded at the current head rather than re-running
// it, which is what makes evaluation safe under Redis at-least-once
// redelivery.
func TestEvaluateIsIdempotent(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	runID := uuid.New()
	headSHA := "aaa"

	var calls int64
	build := func(ctx context.Context, runID uuid.UUID, head gates.RunHead, gate string, params map[string]any) (gates.Input, error) {
		atomic.AddInt64(&calls, 1)
		return gates.Input{
			OrgID: head.OrgID, RepoID: head.RepoID, RunID: runID,
			TargetSHA: head.HeadSHA,
			// No workspace: the tests gate reports "skipped", which is enough
			// to prove the evaluation is cached rather than re-run.
			Exec: analysis.DefaultExec,
		}, nil
	}

	store := newStore(t)
	git := singleGateGit()
	controller := &gates.Controller{
		Store: store,
		Git:   git,
		Runs: func(ctx context.Context, id uuid.UUID) (gates.RunHead, error) {
			return gates.RunHead{OrgID: orgID, RepoID: repoID, TargetRef: "main", HeadSHA: headSHA}, nil
		},
		BuildInput: build,
	}
	srv := gates.NewGRPCServer(controller, nil, nil, nil)
	ctx := scopedCtx(orgID)

	resp1, err := srv.Evaluate(ctx, &gatesv1.EvaluateRequest{RunId: runID.String()})
	if err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}
	if len(resp1.GetEvaluations()) == 0 {
		t.Fatal("first Evaluate returned no evaluations")
	}

	resp2, err := srv.Evaluate(ctx, &gatesv1.EvaluateRequest{RunId: runID.String()})
	if err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if len(resp2.GetEvaluations()) != len(resp1.GetEvaluations()) {
		t.Fatalf("second Evaluate returned %d evaluations, want %d", len(resp2.GetEvaluations()), len(resp1.GetEvaluations()))
	}

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("gate runner invoked %d times across two Evaluate calls, want 1", got)
	}
}

func approvalsStoreForGates(t *testing.T) *approvals.Store {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "approvals", os.DirFS("../approvals/migrations")); err != nil {
		t.Fatalf("migrate approvals schema: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return approvals.NewStore(pool)
}

// TestResolveApprovalRequiresOrgScope asserts a caller cannot resolve an
// approval request that belongs to another organization: ResolveApproval
// first confirms the request is pending within the caller's own org (via
// Pending, which is itself org-scoped), refusing with NotFound otherwise.
func TestResolveApprovalRequiresOrgScope(t *testing.T) {
	approvalsStore := approvalsStoreForGates(t)
	store := newStore(t)
	controller := &gates.Controller{Store: store}
	srv := gates.NewGRPCServer(controller, approvalsStore, nil, nil)

	orgID := uuid.New()
	runID := uuid.New()
	created, err := srv.RequestApproval(scopedCtx(orgID), &gatesv1.RequestApprovalRequest{
		RunId:  runID.String(),
		Action: "deploy_production",
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	foreignOrg := uuid.New()
	_, err = srv.ResolveApproval(scopedCtx(foreignOrg), &gatesv1.ResolveApprovalRequest{
		Id:        created.GetRequest().GetId(),
		DecidedBy: uuid.New().String(),
		Decision:  "approved",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("ResolveApproval for foreign org: code = %v, want NotFound", status.Code(err))
	}

	_, err = srv.ResolveApproval(scopedCtx(orgID), &gatesv1.ResolveApprovalRequest{
		Id:        created.GetRequest().GetId(),
		DecidedBy: uuid.New().String(),
		Decision:  "approved",
	})
	if err != nil {
		t.Fatalf("ResolveApproval for the request's own org: %v", err)
	}
}

// TestEvaluateRecordsProof pins that an evaluation reaches the run's proof,
// on the first evaluation and again when the result is served from cache.
// Evaluations were stored in the gates schema and shown nowhere.
func TestEvaluateRecordsProof(t *testing.T) {
	orgID, repoID, runID := uuid.New(), uuid.New(), uuid.New()
	type proof struct{ gate, status string }
	var proofs []proof
	controller := &gates.Controller{
		Store: newStore(t),
		Git:   singleGateGit(),
		Runs: func(ctx context.Context, id uuid.UUID) (gates.RunHead, error) {
			return gates.RunHead{OrgID: orgID, RepoID: repoID, TargetRef: "main", HeadSHA: "bbb"}, nil
		},
		BuildInput: func(ctx context.Context, runID uuid.UUID, head gates.RunHead, gate string, params map[string]any) (gates.Input, error) {
			return gates.Input{OrgID: head.OrgID, RepoID: head.RepoID, RunID: runID, TargetSHA: head.HeadSHA, Exec: analysis.DefaultExec}, nil
		},
		Proof: func(ctx context.Context, id uuid.UUID, gate, status, detail string) error {
			if id != runID {
				t.Errorf("proof for run %s, want %s", id, runID)
			}
			proofs = append(proofs, proof{gate, status})
			return nil
		},
	}
	ctx := scopedCtx(orgID)
	first, err := controller.Evaluate(ctx, runID)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(first) == 0 || len(proofs) != len(first) {
		t.Fatalf("evaluations %d, proofs %d; want one proof per evaluation", len(first), len(proofs))
	}
	if _, err := controller.Evaluate(ctx, runID); err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if len(proofs) != 2*len(first) {
		t.Fatalf("proofs after a cached evaluation = %d, want %d", len(proofs), 2*len(first))
	}
	if proofs[0].status == "" {
		t.Fatalf("proof carries no status: %+v", proofs[0])
	}
}
