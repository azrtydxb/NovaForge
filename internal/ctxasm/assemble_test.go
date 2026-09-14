package ctxasm

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
	"github.com/novaforge/novaforge/internal/work"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

// testEnv wires a real Postgres-backed graph.Store, graph.VectorStore and
// knowledge.Store — Postgres is never stubbed — sharing one pool since both
// schemas live in the same database, addressed by schema-qualified SQL.
type testEnv struct {
	pool      *pgxpool.Pool
	graphs    *graph.Store
	vectors   *graph.VectorStore
	knowledge *knowledge.Store
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "graph", os.DirFS("../graph/migrations")); err != nil {
		t.Fatalf("migrate graph: %v", err)
	}
	if err := database.Migrate(url, "knowledge", os.DirFS("../knowledge/migrations")); err != nil {
		t.Fatalf("migrate knowledge: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return testEnv{
		pool:      pool,
		graphs:    graph.NewStore(pool),
		vectors:   graph.NewVectorStore(pool),
		knowledge: knowledge.NewStore(pool),
	}
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorKind: "user"})
}

// unitVector returns a deterministic EmbeddingDim-wide embedding stand-in — a
// legitimate in-process double for an external embedding model.
func unitVector(hot int) []float32 {
	v := make([]float32, graph.EmbeddingDim)
	v[hot%graph.EmbeddingDim] = 1
	return v
}

// stubReranker is a deterministic in-process test double for an external
// reranking model: it scores every doc by its position, preserving input
// order, so tests can reason about which candidates were fed to it.
type stubReranker struct{ failing bool }

func (r stubReranker) Rank(_ context.Context, _ string, docs []string) ([]float32, error) {
	if r.failing {
		return nil, fmt.Errorf("stub reranker: refused")
	}
	scores := make([]float32, len(docs))
	for i := range docs {
		scores[i] = float32(len(docs) - i)
	}
	return scores, nil
}

// fakeGitClient is a deterministic in-process test double for the git
// service's gRPC client. It embeds the nil interface so any method beyond
// ListCommits (which ctxasm never calls) panics loudly if exercised by
// mistake, rather than silently doing the wrong thing.
type fakeGitClient struct {
	gitv1.GitServiceClient
	commits []*gitv1.Commit
	err     error
}

func (f *fakeGitClient) ListCommits(_ context.Context, _ *gitv1.ListCommitsRequest, _ ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &gitv1.ListCommitsResponse{Commits: f.commits}, nil
}

// seedFile writes one file's symbol nodes, edges and code chunks through
// the real graph and vector stores, exactly as the indexer would.
func seedFile(t *testing.T, env testEnv, ctx context.Context, orgID, repoID uuid.UUID, path string, nodes []graph.Node, edges []graph.Edge, chunks []graph.Chunk) {
	t.Helper()
	if err := env.graphs.ReplaceFileSubgraph(ctx, orgID, repoID, path, nodes, edges); err != nil {
		t.Fatalf("seed ReplaceFileSubgraph(%s): %v", path, err)
	}
	if len(chunks) > 0 {
		if err := env.vectors.Upsert(ctx, orgID, repoID, path, chunks); err != nil {
			t.Fatalf("seed Vectors.Upsert(%s): %v", path, err)
		}
	}
}

func symbolNode(orgID uuid.UUID, path, name string, start, end int) graph.Node {
	return graph.Node{
		ID:    uuid.New(),
		OrgID: orgID,
		Kind:  "symbol",
		Key:   uuid.NewString(),
		Attrs: map[string]string{
			"path": path, "name": name, "kind": "function",
			"signature":  fmt.Sprintf("func %s()", name),
			"start_line": fmt.Sprint(start), "end_line": fmt.Sprint(end),
		},
	}
}

func TestBundleRespectsTokenBudget(t *testing.T) {
	env := newTestEnv(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	// Seed enough matching data across four signals (lexical, symbol,
	// history, semantic) that, uncapped, the candidate pool would exceed
	// 200 entries — far more than a 1000-token budget can hold.
	for i := 0; i < 60; i++ {
		path := fmt.Sprintf("pkg/widget_%d.go", i)
		sym := symbolNode(orgID, path, fmt.Sprintf("Widget%d", i), 1, 5)
		chunk := graph.Chunk{
			ID: uuid.New(), Path: path, StartLine: 1, EndLine: 5,
			Text:      fmt.Sprintf("widget widget widget number %d handles widget assembly", i),
			Embedding: unitVector(i),
		}
		seedFile(t, env, ctx, orgID, repoID, path, []graph.Node{sym}, nil, []graph.Chunk{chunk})
	}

	commits := make([]*gitv1.Commit, 0, 60)
	for i := 0; i < 60; i++ {
		commits = append(commits, &gitv1.Commit{Sha: fmt.Sprintf("sha%d", i), Message: fmt.Sprintf("widget commit number %d about widgets", i)})
	}

	in := Input{
		OrgID:       orgID,
		RepoID:      repoID,
		WorkItem:    work.Item{Goal: "Improve widget assembly performance for the widget subsystem"},
		TokenBudget: 1000,
		Git:         &fakeGitClient{commits: commits},
		Graph:       env.graphs,
		Vectors:     env.vectors,
		Knowledge:   env.knowledge,
		Reranker:    stubReranker{},
	}

	bundle, err := Assemble(context.Background(), in)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if bundle.TokensEstimated > 1000 {
		t.Fatalf("TokensEstimated = %d, want <= 1000", bundle.TokensEstimated)
	}
	if len(bundle.Files) == 0 {
		t.Fatalf("expected a non-empty bundle")
	}
}

func TestBundleExcludesUnrelatedFiles(t *testing.T) {
	env := newTestEnv(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	authSym := symbolNode(orgID, "internal/auth/service.go", "ValidateToken", 10, 20)
	authChunk := graph.Chunk{
		ID: uuid.New(), Path: "internal/auth/service.go", StartLine: 10, EndLine: 20,
		Text:      "func ValidateToken validates a JWT authentication token for the AuthService",
		Embedding: unitVector(1),
	}
	seedFile(t, env, ctx, orgID, repoID, "internal/auth/service.go", []graph.Node{authSym}, nil, []graph.Chunk{authChunk})

	billingSym := symbolNode(orgID, "internal/billing/service.go", "ComputeInvoiceTotal", 5, 15)
	billingChunk := graph.Chunk{
		ID: uuid.New(), Path: "internal/billing/service.go", StartLine: 5, EndLine: 15,
		Text:      "func ComputeInvoiceTotal sums invoice line items for monthly billing",
		Embedding: unitVector(2),
	}
	seedFile(t, env, ctx, orgID, repoID, "internal/billing/service.go", []graph.Node{billingSym}, nil, []graph.Chunk{billingChunk})

	in := Input{
		OrgID:       orgID,
		RepoID:      repoID,
		WorkItem:    work.Item{Goal: "Fix JWT authentication token validation in AuthService"},
		TokenBudget: 10000,
		Git:         &fakeGitClient{commits: []*gitv1.Commit{{Sha: "s1", Message: "tune auth token expiry"}}},
		Graph:       env.graphs,
		Vectors:     env.vectors,
		Knowledge:   env.knowledge,
		Reranker:    stubReranker{},
	}

	bundle, err := Assemble(context.Background(), in)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(bundle.Files) == 0 {
		t.Fatalf("expected some related files, got none")
	}
	for _, f := range bundle.Files {
		if f.Path == "internal/billing/service.go" {
			t.Fatalf("unrelated file leaked into bundle: %+v", f)
		}
	}
}

func TestAllSixSignalsContribute(t *testing.T) {
	env := newTestEnv(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	sym := symbolNode(orgID, "internal/checkout/flow.go", "ProcessCheckout", 1, 40)
	dep := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "service", Key: uuid.NewString(), Attrs: map[string]string{"name": "PaymentGateway"}}
	test := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "test", Key: uuid.NewString(), Attrs: map[string]string{"path": "internal/checkout/flow_test.go"}}
	edges := []graph.Edge{
		{FromID: sym.ID, ToID: dep.ID, Kind: "depends_on"},
		{FromID: sym.ID, ToID: test.ID, Kind: "tested_by"},
	}
	chunk := graph.Chunk{
		ID: uuid.New(), Path: "internal/checkout/flow.go", StartLine: 1, EndLine: 40,
		Text:      "func ProcessCheckout runs the checkout flow end to end for a cart",
		Embedding: unitVector(3),
	}
	seedFile(t, env, ctx, orgID, repoID, "internal/checkout/flow.go", []graph.Node{sym, dep, test}, edges, []graph.Chunk{chunk})

	in := Input{
		OrgID:    orgID,
		RepoID:   repoID,
		WorkItem: work.Item{Goal: "Fix the checkout flow so ProcessCheckout handles a declined payment"},
		Git:      &fakeGitClient{commits: []*gitv1.Commit{{Sha: "s1", Message: "checkout flow retry logic"}}},
		Graph:    env.graphs,
		Vectors:  env.vectors,
	}

	candidates := gatherCandidates(context.Background(), in)
	seen := map[string]bool{}
	for _, c := range candidates {
		seen[c.Signal] = true
	}
	for _, want := range []string{"lexical", "symbol", "dependency", "semantic", "history", "test"} {
		if !seen[want] {
			t.Errorf("no candidate tagged %q; got signals %v", want, seen)
		}
	}
}

func TestStaleIndexDegradesNotFails(t *testing.T) {
	env := newTestEnv(t)
	orgID := uuid.New()
	repoID := uuid.New()

	// No symbol/chunk data is seeded for this org at all — an empty index —
	// yet Assemble must still succeed, degrading to whatever the remaining
	// signals (here, none) can offer.
	in := Input{
		OrgID:       orgID,
		RepoID:      repoID,
		WorkItem:    work.Item{Goal: "Investigate a mysterious latency spike"},
		TokenBudget: 1000,
		Git:         &fakeGitClient{},
		Graph:       env.graphs,
		Vectors:     env.vectors,
		Knowledge:   env.knowledge,
		Reranker:    stubReranker{},
	}

	bundle, err := Assemble(context.Background(), in)
	if err != nil {
		t.Fatalf("Assemble on empty index should degrade, not fail: %v", err)
	}
	if len(bundle.Files) != 0 {
		t.Fatalf("expected no files from an empty index, got %+v", bundle.Files)
	}
}

func TestKnowledgeIsIncluded(t *testing.T) {
	env := newTestEnv(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	anchor := unitVector(7)
	sym := symbolNode(orgID, "internal/deploy/rollout.go", "RollingDeploy", 1, 10)
	chunk := graph.Chunk{
		ID: uuid.New(), Path: "internal/deploy/rollout.go", StartLine: 1, EndLine: 10,
		Text:      "func RollingDeploy performs a rolling deploy across the fleet",
		Embedding: anchor,
	}
	seedFile(t, env, ctx, orgID, repoID, "internal/deploy/rollout.go", []graph.Node{sym}, nil, []graph.Chunk{chunk})

	entry, err := env.knowledge.Record(ctx, knowledge.Entry{
		OrgID: orgID, RepoID: repoID, Key: "deploy-decision", Kind: "decision",
		Title: "Never validate JWT tokens directly in route handlers",
		Body:  "Always roll out behind a health-checked canary before shifting traffic.",
	}, anchor)
	if err != nil {
		t.Fatalf("Record knowledge: %v", err)
	}

	in := Input{
		OrgID:       orgID,
		RepoID:      repoID,
		WorkItem:    work.Item{Goal: "Perform a rolling deploy of the fleet"},
		TokenBudget: 1000,
		Git:         &fakeGitClient{},
		Graph:       env.graphs,
		Vectors:     env.vectors,
		Knowledge:   env.knowledge,
		Reranker:    stubReranker{},
	}

	bundle, err := Assemble(context.Background(), in)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	found := false
	for _, k := range bundle.Knowledge {
		if k.ID == entry.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected knowledge entry %s in bundle, got %+v", entry.ID, bundle.Knowledge)
	}
}
