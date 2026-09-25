package reviews_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/azrtydxb/go-ai-sdk/provider"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/reviews"
)

// stubReviewModel is an in-process test double for provider.LanguageModel:
// it always returns the same canned JSON verdict/summary body, and reports
// a caller-supplied id so tests can tell which configured model answered a
// given role.
type stubReviewModel struct {
	id   string
	body string
}

func (m *stubReviewModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	return &provider.Response{
		Content:      []provider.ContentPart{provider.TextPart{Text: m.body}},
		FinishReason: provider.FinishStop,
	}, nil
}

func (m *stubReviewModel) Stream(ctx context.Context, call provider.Call) (provider.StreamResponse, error) {
	return nil, fmt.Errorf("stub review model does not support streaming")
}

func (m *stubReviewModel) ModelID() string      { return m.id }
func (m *stubReviewModel) ProviderName() string { return "stub" }
func (m *stubReviewModel) Capabilities() provider.Capabilities {
	return provider.Capabilities{NativeJSON: true}
}

func approveModel(id string) *stubReviewModel {
	return &stubReviewModel{id: id, body: `{"verdict":"approve","summary":"looks good"}`}
}

func requestChangesModel(id string) *stubReviewModel {
	return &stubReviewModel{id: id, body: `{"verdict":"request_changes","summary":"needs work"}`}
}

// runWithAuthorAgent creates a run authored by an agent (authorAgentID),
// so ReviewRun's author-exclusion logic has something to exclude.
func runWithAuthorAgent(t *testing.T, store *reviews.Store, ctx context.Context, orgID, authorAgentID uuid.UUID) reviews.Run {
	t.Helper()
	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorAgentID, AuthorKind: "agent", AgentName: "backend-agent",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func TestAuthorAgentExcludedFromReviewers(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorAgentID := uuid.New()
	ctx := proofServiceContext(t, scopedCtx(orgID), "work-reviews")

	run := runWithAuthorAgent(t, store, ctx, orgID, authorAgentID)

	securityAgentID := uuid.New()
	r := &reviews.AgentReviewer{
		Git:   &stubMergeGitClient{},
		Store: store,
		RoleAgent: map[string]agents.Agent{
			"reviewer": {ID: authorAgentID, Name: "backend-agent", Role: "reviewer"},
			"security": {ID: securityAgentID, Name: "security-agent", Role: "security"},
		},
		Models: []provider.LanguageModel{approveModel("m1")},
	}

	verdicts, err := r.ReviewRun(ctx, run.ID, []string{"reviewer", "security"})
	if err != nil {
		t.Fatalf("ReviewRun: %v", err)
	}
	for _, v := range verdicts {
		if v.AgentName == "backend-agent" {
			t.Fatalf("want the author agent never dispatched as a reviewer, got verdict from it: %+v", v)
		}
	}
	if len(verdicts) != 1 || verdicts[0].Role != "security" {
		t.Fatalf("want exactly the security verdict, got %+v", verdicts)
	}
}

func TestReviewersUseDistinctModelsWhenAvailable(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorID := uuid.New()
	ctx := proofServiceContext(t, scopedCtx(orgID), "work-reviews")

	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	r := &reviews.AgentReviewer{
		Git:   &stubMergeGitClient{},
		Store: store,
		RoleAgent: map[string]agents.Agent{
			"reviewer": {ID: uuid.New(), Name: "reviewer-agent", Role: "reviewer"},
			"security": {ID: uuid.New(), Name: "security-agent", Role: "security"},
		},
		Models: []provider.LanguageModel{approveModel("model-a"), approveModel("model-b")},
	}

	verdicts, err := r.ReviewRun(ctx, run.ID, []string{"reviewer", "security"})
	if err != nil {
		t.Fatalf("ReviewRun: %v", err)
	}
	if len(verdicts) != 2 {
		t.Fatalf("want 2 verdicts, got %d", len(verdicts))
	}
	if verdicts[0].ModelName == verdicts[1].ModelName {
		t.Fatalf("want reviewer and security verdicts to use distinct models, both used %q", verdicts[0].ModelName)
	}
}

func TestRequestChangesBlocksMerge(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorID := uuid.New()
	ctx := proofServiceContext(t, scopedCtx(orgID), "work-reviews")

	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	r := &reviews.AgentReviewer{
		Git:   &stubMergeGitClient{},
		Store: store,
		RoleAgent: map[string]agents.Agent{
			"reviewer": {ID: uuid.New(), Name: "reviewer-agent", Role: "reviewer"},
		},
		Models: []provider.LanguageModel{requestChangesModel("m1")},
	}
	if _, err := r.ReviewRun(ctx, run.ID, []string{"reviewer"}); err != nil {
		t.Fatalf("ReviewRun: %v", err)
	}

	git := &stubMergeGitClient{mergeSHA: "deadbeef"}
	m := &reviews.Merger{
		Store: store,
		Gates: stubGateChecker{allowed: true},
		Git:   git,
	}
	_, err = m.Merge(ctx, run.ID, "merge")
	if err == nil {
		t.Fatal("want merge blocked: the only recorded verdict was request_changes, not an independent approval")
	}
	if git.called {
		t.Fatal("want git merge RPC never called without an independent approval")
	}
}

// reviewGatesGit stubs a repository whose main branch declares one required
// gate ("tests"), for TestAllApprovalsStillRequireGates.
type reviewGatesGit struct {
	gitv1.GitServiceClient
}

func (reviewGatesGit) GetTree(_ context.Context, in *gitv1.GetTreeRequest, _ ...grpc.CallOption) (*gitv1.GetTreeResponse, error) {
	if in.Ref != "main" {
		return &gitv1.GetTreeResponse{}, nil
	}
	return &gitv1.GetTreeResponse{Entries: []*gitv1.TreeEntry{{Kind: "blob", Name: "tests.yaml"}}}, nil
}

func (reviewGatesGit) GetBlob(_ context.Context, in *gitv1.GetBlobRequest, _ ...grpc.CallOption) (*gitv1.GetBlobResponse, error) {
	if in.Ref == "main" && in.Path == ".novaforge/gates/tests.yaml" {
		return &gitv1.GetBlobResponse{Content: []byte("name: tests\nrequired: true\n")}, nil
	}
	return nil, status.Error(codes.NotFound, "not found")
}

func gatesStore(t *testing.T) *gates.Store {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "gates", os.DirFS("../gates/migrations")); err != nil {
		t.Fatalf("Migrate gates: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return gates.NewStore(pool)
}

func TestAllApprovalsStillRequireGates(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorID := uuid.New()
	ctx := proofServiceContext(t, scopedCtx(orgID), "work-reviews")

	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	r := &reviews.AgentReviewer{
		Git:   &stubMergeGitClient{},
		Store: store,
		RoleAgent: map[string]agents.Agent{
			"reviewer":     {ID: uuid.New(), Name: "reviewer-agent", Role: "reviewer"},
			"security":     {ID: uuid.New(), Name: "security-agent", Role: "security"},
			"test":         {ID: uuid.New(), Name: "test-agent", Role: "test"},
			"architecture": {ID: uuid.New(), Name: "architecture-agent", Role: "architecture"},
		},
		Models: []provider.LanguageModel{approveModel("m1")},
	}
	verdicts, err := r.ReviewRun(ctx, run.ID, nil) // DefaultReviewRoles
	if err != nil {
		t.Fatalf("ReviewRun: %v", err)
	}
	if len(verdicts) != 4 {
		t.Fatalf("want 4 approving verdicts, got %d", len(verdicts))
	}
	for _, v := range verdicts {
		if v.Verdict != "approve" {
			t.Fatalf("want every verdict to be approve, got %q from role %q", v.Verdict, v.Role)
		}
	}

	gStore := gatesStore(t)
	head := "aaa"
	controller := &gates.Controller{
		Store: gStore,
		Git:   reviewGatesGit{},
		Runs: func(context.Context, uuid.UUID) (gates.RunHead, error) {
			return gates.RunHead{OrgID: orgID, RepoID: run.RepoID, TargetRef: "main", HeadSHA: head}, nil
		},
	}
	if err := gStore.RecordEvaluation(ctx, gates.Evaluation{
		OrgID: orgID, RunID: run.ID, Gate: "tests", Status: "fail", TargetSHA: head,
	}); err != nil {
		t.Fatalf("RecordEvaluation: %v", err)
	}

	allowed, reasons, err := controller.MayMerge(ctx, run.ID)
	if err != nil {
		t.Fatalf("MayMerge: %v", err)
	}
	if allowed {
		t.Fatalf("want MayMerge false despite 4 approving reviews, since the tests gate failed; reasons=%v", reasons)
	}
}

func TestReviewVerdictsRecordedAsProof(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	authorID := uuid.New()
	ctx := proofServiceContext(t, scopedCtx(orgID), "work-reviews")

	run, err := store.CreateRun(ctx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "r", SourceRef: "src", TargetRef: "main",
		AuthorID: authorID, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	r := &reviews.AgentReviewer{
		Git:   &stubMergeGitClient{},
		Store: store,
		RoleAgent: map[string]agents.Agent{
			"reviewer": {ID: uuid.New(), Name: "reviewer-agent", Role: "reviewer"},
			"security": {ID: uuid.New(), Name: "security-agent", Role: "security"},
		},
		Models: []provider.LanguageModel{approveModel("m1")},
	}
	verdicts, err := r.ReviewRun(ctx, run.ID, []string{"reviewer", "security"})
	if err != nil {
		t.Fatalf("ReviewRun: %v", err)
	}

	proofs, err := store.ListProof(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListProof: %v", err)
	}
	proofGates := make(map[string]bool, len(proofs))
	for _, p := range proofs {
		proofGates[p.Gate] = true
	}
	for _, v := range verdicts {
		if !proofGates["review:"+v.Role] {
			t.Fatalf("want a proof record for role %q, got proofs %v", v.Role, proofGates)
		}
	}
}
