package gates_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/gates"
)

// singleGateGit stubs a repository whose main branch declares one required
// gate ("tests") and whose source branch declares none, so tests can prove
// gate config is read only from the target ref.
func singleGateGit() *stubGitClient {
	return &stubGitClient{
		tree: map[string][]*gitv1.TreeEntry{
			"main": {
				{Kind: "blob", Name: "tests.yaml"},
			},
			"agents/work/x": {},
		},
		blobs: map[string][]byte{
			"main|.novaforge/gates/tests.yaml": []byte("name: tests\nrequired: true\n"),
		},
	}
}

func newController(t *testing.T, git *stubGitClient, headSHA *string) *gates.Controller {
	t.Helper()
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	return &gates.Controller{
		Store: store,
		Git:   git,
		Runs: func(ctx context.Context, runID uuid.UUID) (gates.RunHead, error) {
			return gates.RunHead{
				OrgID:     orgID,
				RepoID:    repoID,
				TargetRef: "main",
				HeadSHA:   *headSHA,
			}, nil
		},
	}
}

func TestMayMergeFalseWhenGateFails(t *testing.T) {
	git := singleGateGit()
	head := "aaa"
	c := newController(t, git, &head)
	runID := uuid.New()

	orgID := headOrg(t, c, runID)
	ctx := scopedCtx(orgID)

	if err := c.Store.RecordEvaluation(ctx, gates.Evaluation{
		OrgID: orgID, RunID: runID, Gate: "tests", Status: "fail", TargetSHA: head,
	}); err != nil {
		t.Fatalf("RecordEvaluation: %v", err)
	}

	allowed, reasons, err := c.MayMerge(ctx, runID)
	if err != nil {
		t.Fatalf("MayMerge: %v", err)
	}
	if allowed {
		t.Fatal("want allowed false when a required gate fails")
	}
	if !containsSubstring(reasons, "tests") {
		t.Fatalf("want a reason naming tests, got %v", reasons)
	}
}

func TestMayMergeFalseWhenGateMissing(t *testing.T) {
	git := singleGateGit()
	head := "aaa"
	c := newController(t, git, &head)
	runID := uuid.New()
	orgID := headOrg(t, c, runID)
	ctx := scopedCtx(orgID)

	allowed, reasons, err := c.MayMerge(ctx, runID)
	if err != nil {
		t.Fatalf("MayMerge: %v", err)
	}
	if allowed {
		t.Fatal("want allowed false when a required gate was never evaluated")
	}
	if !containsSubstring(reasons, "not evaluated") {
		t.Fatalf("want a reason containing 'not evaluated', got %v", reasons)
	}
}

func TestMayMergeFalseWhenEvaluationIsStale(t *testing.T) {
	git := singleGateGit()
	head := "aaa"
	c := newController(t, git, &head)
	runID := uuid.New()
	orgID := headOrg(t, c, runID)
	ctx := scopedCtx(orgID)

	if err := c.Store.RecordEvaluation(ctx, gates.Evaluation{
		OrgID: orgID, RunID: runID, Gate: "tests", Status: "pass", TargetSHA: "aaa",
	}); err != nil {
		t.Fatalf("RecordEvaluation: %v", err)
	}

	// Advance the run's head to a new SHA without a matching evaluation.
	head = "bbb"

	allowed, reasons, err := c.MayMerge(ctx, runID)
	if err != nil {
		t.Fatalf("MayMerge: %v", err)
	}
	if allowed {
		t.Fatal("want allowed false when the only pass is stale (a different SHA)")
	}
	if len(reasons) == 0 {
		t.Fatal("want a reason for the stale evaluation")
	}
}

func TestMayMergeTrueWhenAllPass(t *testing.T) {
	git := singleGateGit()
	head := "aaa"
	c := newController(t, git, &head)
	runID := uuid.New()
	orgID := headOrg(t, c, runID)
	ctx := scopedCtx(orgID)

	if err := c.Store.RecordEvaluation(ctx, gates.Evaluation{
		OrgID: orgID, RunID: runID, Gate: "tests", Status: "pass", TargetSHA: head,
	}); err != nil {
		t.Fatalf("RecordEvaluation: %v", err)
	}

	allowed, reasons, err := c.MayMerge(ctx, runID)
	if err != nil {
		t.Fatalf("MayMerge: %v", err)
	}
	if !allowed {
		t.Fatalf("want allowed true when every required gate passes at head, got reasons %v", reasons)
	}
}

func TestGateConfigEditedInSourceIgnored(t *testing.T) {
	git := &stubGitClient{
		tree: map[string][]*gitv1.TreeEntry{
			"main": {
				{Kind: "blob", Name: "tests.yaml"},
			},
			// The source branch declares zero gates: an agent trying to
			// delete the gate that judges it must not succeed.
			"agents/work/x": {},
		},
		blobs: map[string][]byte{
			"main|.novaforge/gates/tests.yaml": []byte("name: tests\nrequired: true\n"),
		},
	}
	head := "aaa"
	c := newController(t, git, &head)
	runID := uuid.New()
	orgID := headOrg(t, c, runID)
	ctx := scopedCtx(orgID)

	// No evaluation recorded at all.
	allowed, reasons, err := c.MayMerge(ctx, runID)
	if err != nil {
		t.Fatalf("MayMerge: %v", err)
	}
	if allowed {
		t.Fatal("want allowed false: the target ref's gate still applies regardless of what the source declares")
	}
	if len(reasons) == 0 {
		t.Fatal("want a reason for the unevaluated target-ref gate")
	}
}

// headOrg extracts the OrgID the controller's Runs stub will report, so
// tests can build an authz-scoped context matching it.
func headOrg(t *testing.T, c *gates.Controller, runID uuid.UUID) uuid.UUID {
	t.Helper()
	head, err := c.Runs(context.Background(), runID)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	return head.OrgID
}

func containsSubstring(items []string, substr string) bool {
	for _, item := range items {
		if strings.Contains(item, substr) {
			return true
		}
	}
	return false
}
