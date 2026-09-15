package graph_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
)

func newGRPCServer(t *testing.T) *graph.GRPCServer {
	t.Helper()
	store := newStore(t)
	return graph.NewGRPCServer(store, nil, nil, nil, nil, nil)
}

func seedSymbol(t *testing.T, store *graph.Store, ctx context.Context, orgID, repoID uuid.UUID, path, name string) graph.Node {
	t.Helper()
	sym := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "symbol", Key: uniqueKey(name), Attrs: map[string]string{
		"path": path, "name": name, "kind": "function", "signature": "func " + name + "()",
		"start_line": "1", "end_line": "10",
	}}
	if err := store.ReplaceFileSubgraph(ctx, orgID, repoID, path, []graph.Node{sym}, nil); err != nil {
		t.Fatalf("seed symbol %s: %v", name, err)
	}
	return sym
}

func TestDependentsAnswersForSymbol(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	userService := seedSymbol(t, srv.Store, ctx, orgID, repoID, "internal/user/service.go", "UserService")
	apiNode := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "service", Key: uniqueKey("api")}
	if _, err := srv.Store.UpsertNode(ctx, apiNode); err != nil {
		t.Fatalf("UpsertNode api: %v", err)
	}
	if err := srv.Store.UpsertEdge(ctx, graph.Edge{FromID: apiNode.ID, ToID: userService.ID, Kind: "depends_on"}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	resp, err := srv.Dependents(ctx, &graphv1.DependentsRequest{RepoId: repoID.String(), Symbol: "UserService"})
	if err != nil {
		t.Fatalf("Dependents: %v", err)
	}
	if len(resp.GetNodes()) != 1 || resp.GetNodes()[0].GetId() != apiNode.ID.String() {
		t.Fatalf("Dependents = %+v, want [api]", resp.GetNodes())
	}
}

func TestTestsCoveringAnswersForSymbol(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	sym := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "symbol", Key: uniqueKey("Widget"), Attrs: map[string]string{
		"path": "pkg/widget.go", "name": "Widget",
	}}
	test := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "test", Key: uniqueKey("widget_test"), Attrs: map[string]string{
		"path": "pkg/widget_test.go",
	}}
	edges := []graph.Edge{{FromID: sym.ID, ToID: test.ID, Kind: "tested_by"}}
	if err := srv.Store.ReplaceFileSubgraph(ctx, orgID, repoID, "pkg/widget.go", []graph.Node{sym, test}, edges); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := srv.TestsCovering(ctx, &graphv1.TestsCoveringRequest{RepoId: repoID.String(), Symbol: "Widget"})
	if err != nil {
		t.Fatalf("TestsCovering: %v", err)
	}
	if len(resp.GetTests()) != 1 || resp.GetTests()[0] != "pkg/widget_test.go" {
		t.Fatalf("TestsCovering = %v, want [pkg/widget_test.go]", resp.GetTests())
	}
}

func TestLastChangedByReturnsWorkItem(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	sym := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "symbol", Key: uniqueKey("Billing"), Attrs: map[string]string{
		"path": "pkg/billing.go", "name": "Billing",
	}}
	older := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "work_item", Key: uniqueKey("NF-1"), Attrs: map[string]string{
		"key": "NF-1", "changed_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}}
	newer := graph.Node{ID: uuid.New(), OrgID: orgID, Kind: "work_item", Key: uniqueKey("NF-2"), Attrs: map[string]string{
		"key": "NF-2", "changed_at": time.Now().UTC().Format(time.RFC3339),
	}}
	edges := []graph.Edge{
		{FromID: sym.ID, ToID: older.ID, Kind: "changed_by"},
		{FromID: sym.ID, ToID: newer.ID, Kind: "changed_by"},
	}
	if err := srv.Store.ReplaceFileSubgraph(ctx, orgID, repoID, "pkg/billing.go", []graph.Node{sym, older, newer}, edges); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := srv.LastChangedBy(ctx, &graphv1.LastChangedByRequest{RepoId: repoID.String(), Symbol: "Billing"})
	if err != nil {
		t.Fatalf("LastChangedBy: %v", err)
	}
	if resp.GetWorkItemKey() != "NF-2" {
		t.Fatalf("LastChangedBy = %q, want NF-2 (the more recent edge)", resp.GetWorkItemKey())
	}
}

func TestGraphQueriesRejectForeignOrg(t *testing.T) {
	srv := newGRPCServer(t)
	orgA := uuid.New()
	orgB := uuid.New()
	repoID := uuid.New()
	ctxA := scopedCtx(orgA, uuid.New())
	ctxB := scopedCtx(orgB, uuid.New())

	seedSymbol(t, srv.Store, ctxA, orgA, repoID, "internal/secret/service.go", "SecretService")

	_, err := srv.GetSymbol(ctxB, &graphv1.GetSymbolRequest{RepoId: repoID.String(), Name: "SecretService"})
	if err == nil {
		t.Fatalf("expected an error for a symbol belonging to another org, got none")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied: %v", status.Code(err), err)
	}
}

func TestGraphQueriesRequireScope(t *testing.T) {
	srv := newGRPCServer(t)
	repoID := uuid.New()

	_, err := srv.GetSymbol(context.Background(), &graphv1.GetSymbolRequest{RepoId: repoID.String(), Name: "Anything"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied for a call with no scope at all", status.Code(err))
	}
}

func TestGetSymbolAnswersForKnownSymbol(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	seedSymbol(t, srv.Store, ctx, orgID, repoID, "internal/order/service.go", "OrderService")

	resp, err := srv.GetSymbol(ctx, &graphv1.GetSymbolRequest{RepoId: repoID.String(), Name: "OrderService"})
	if err != nil {
		t.Fatalf("GetSymbol: %v", err)
	}
	if resp.GetSymbol().GetName() != "OrderService" || resp.GetSymbol().GetPath() != "internal/order/service.go" {
		t.Fatalf("GetSymbol = %+v, want OrderService at internal/order/service.go", resp.GetSymbol())
	}
}

func TestGetSymbolNotFound(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	_, err := srv.GetSymbol(ctx, &graphv1.GetSymbolRequest{RepoId: repoID.String(), Name: "DoesNotExist"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestSearchCodeFallsBackToLexicalWithoutEmbedder(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	vs := newVectorStore(t)
	// grpc_test.go's server was built on a fresh store/pool via newStore,
	// but code_chunks and graph_nodes live in the same schema, so reuse the
	// server's own store pool for the vector store to keep data visible to
	// the RPC under test.
	vs = graph.NewVectorStore(srv.Store.Pool())
	placeholderEmbedding := make([]float32, graph.EmbeddingDim)
	if err := vs.Upsert(ctx, orgID, repoID, "pkg/search.go", []graph.Chunk{
		{ID: uuid.New(), Path: "pkg/search.go", StartLine: 1, EndLine: 3, Text: "func FindWidget locates a widget by id", Embedding: placeholderEmbedding},
	}); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}

	resp, err := srv.SearchCode(ctx, &graphv1.SearchCodeRequest{RepoId: repoID.String(), Query: "widget", K: 10})
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	if len(resp.GetChunks()) != 1 || resp.GetChunks()[0].GetPath() != "pkg/search.go" {
		t.Fatalf("SearchCode = %+v, want the seeded chunk", resp.GetChunks())
	}
	if resp.GetMode() != "lexical" {
		t.Fatalf("mode = %q, want lexical: a substring match must not present itself as semantic", resp.GetMode())
	}
}

// TestSearchCodeSaysWhenItIsSemantic checks the other half: with an embedding
// model answering, results come from vector distance and say so. A query that
// shares no word with the chunk it should find proves the lexical path did not
// answer it.
func TestSearchCodeSaysWhenItIsSemantic(t *testing.T) {
	pool := storePool(t)
	orgID, repoID := uuid.New(), uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	query := "tax calculation on a bill"
	embedder := &stubEmbedder{vectors: map[string][]float32{query: unitVector(graph.EmbeddingDim, 3)}}
	srv := graph.NewGRPCServer(graph.NewStore(pool), graph.NewVectorStore(pool), nil, nil, embedder, nil)

	vs := graph.NewVectorStore(pool)
	if err := vs.Upsert(ctx, orgID, repoID, "billing/vat.go", []graph.Chunk{
		{Path: "billing/vat.go", StartLine: 1, EndLine: 3, Text: "func VATTotal(net float64) float64", Embedding: unitVector(graph.EmbeddingDim, 3)},
	}); err != nil {
		t.Fatalf("seed vat chunk: %v", err)
	}
	if err := vs.Upsert(ctx, orgID, repoID, "config/toml.go", []graph.Chunk{
		{Path: "config/toml.go", StartLine: 1, EndLine: 3, Text: "func ParseTOML(b []byte)", Embedding: unitVector(graph.EmbeddingDim, 9)},
	}); err != nil {
		t.Fatalf("seed toml chunk: %v", err)
	}

	resp, err := srv.SearchCode(ctx, &graphv1.SearchCodeRequest{RepoId: repoID.String(), Query: query, K: 5})
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	if resp.GetMode() != "semantic" {
		t.Fatalf("mode = %q, want semantic", resp.GetMode())
	}
	if len(resp.GetChunks()) == 0 || resp.GetChunks()[0].GetPath() != "billing/vat.go" {
		t.Fatalf("SearchCode = %+v, want billing/vat.go first", resp.GetChunks())
	}
}

func TestRecordAndSearchKnowledgeWithoutEmbedderDegrades(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	pool := storePool(t)
	store := graph.NewStore(pool)
	knowledgeStore := newKnowledgeStoreForGRPCTest(t, pool)
	srv := graph.NewGRPCServer(store, graph.NewVectorStore(pool), knowledgeStore, nil, nil, nil)
	ctx := scopedCtx(orgID, uuid.New())

	rec, err := srv.RecordKnowledge(ctx, &graphv1.RecordKnowledgeRequest{
		RepoId: repoID.String(), Key: "k1", Kind: "decision", Title: "Title", Body: "Body",
	})
	if err != nil {
		t.Fatalf("RecordKnowledge: %v", err)
	}
	if rec.GetId() == "" {
		t.Fatalf("expected a non-empty id")
	}

	// This test used to pin that SearchKnowledge answers nothing without an
	// Embedder — which meant that on a deployment with no embedding model
	// every recorded decision was invisible to people and agents alike. A
	// query that shares no word with the entry still finds nothing.
	searchResp, err := srv.SearchKnowledge(ctx, &graphv1.SearchKnowledgeRequest{RepoId: repoID.String(), Query: "anything"})
	if err != nil {
		t.Fatalf("SearchKnowledge: %v", err)
	}
	if len(searchResp.GetEntries()) != 0 || searchResp.GetMode() != "text" {
		t.Fatalf("an unrelated query found %+v (mode %q), want nothing by text", searchResp.GetEntries(), searchResp.GetMode())
	}

	// With no query the newest entries are listed, and a query sharing the
	// entry's words finds it by text.
	listed, err := srv.SearchKnowledge(ctx, &graphv1.SearchKnowledgeRequest{RepoId: repoID.String()})
	if err != nil {
		t.Fatalf("SearchKnowledge (no query): %v", err)
	}
	if len(listed.GetEntries()) != 1 || listed.GetEntries()[0].GetId() != rec.GetId() || listed.GetMode() != "recent" {
		t.Fatalf("listing = %+v (mode %q), want the recorded entry", listed.GetEntries(), listed.GetMode())
	}
	runID := uuid.New()
	if _, err := srv.RecordKnowledge(ctx, &graphv1.RecordKnowledgeRequest{
		RepoId: repoID.String(), Key: "decision/retries", Kind: "decision",
		Title: "Webhook deliveries retry with backoff", Body: "Retries back off exponentially, capped at one hour.", SourceRunId: runID.String(),
	}); err != nil {
		t.Fatalf("RecordKnowledge with a source run: %v", err)
	}
	found, err := srv.SearchKnowledge(ctx, &graphv1.SearchKnowledgeRequest{RepoId: repoID.String(), Query: "how do webhook retries work"})
	if err != nil {
		t.Fatalf("SearchKnowledge (text): %v", err)
	}
	if len(found.GetEntries()) != 1 || found.GetEntries()[0].GetSourceRunId() != runID.String() {
		t.Fatalf("text search = %+v, want the webhook decision recorded by run %s", found.GetEntries(), runID)
	}
	if _, err := srv.RecordKnowledge(ctx, &graphv1.RecordKnowledgeRequest{RepoId: repoID.String(), Key: "x", Kind: "musing", Title: "t", Body: "b"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("an unknown kind = %v, want InvalidArgument", err)
	}
}

func TestAssembleContextRequiresConfiguration(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	_, err := srv.AssembleContext(ctx, &graphv1.AssembleContextRequest{RepoId: repoID.String(), WorkItemKey: "NF-1"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable when no AssembleContextFunc is configured", status.Code(err))
	}
}

func TestAssembleContextDelegatesToConfiguredFunc(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	var gotOrg, gotRepo uuid.UUID
	var gotKey string
	assemble := func(_ context.Context, o, r uuid.UUID, key string, _ int) (graph.ContextBundle, error) {
		gotOrg, gotRepo, gotKey = o, r, key
		return graph.ContextBundle{Files: []graph.ContextSnippet{{Path: "a.go", Text: "x"}}, TokensEstimated: 1}, nil
	}
	srv := graph.NewGRPCServer(store, nil, nil, nil, nil, assemble)

	resp, err := srv.AssembleContext(ctx, &graphv1.AssembleContextRequest{RepoId: repoID.String(), WorkItemKey: "NF-7", TokenBudget: 500})
	if err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}
	if gotOrg != orgID || gotRepo != repoID || gotKey != "NF-7" {
		t.Fatalf("assemble called with (%s,%s,%s), want (%s,%s,NF-7)", gotOrg, gotRepo, gotKey, orgID, repoID)
	}
	if len(resp.GetBundle().GetFiles()) != 1 {
		t.Fatalf("expected the bundle to pass through, got %+v", resp.GetBundle())
	}
}

// newKnowledgeStoreForGRPCTest migrates the knowledge schema into the same
// database pool already open for the graph schema and returns a
// knowledge.Store backed by it.
func newKnowledgeStoreForGRPCTest(t *testing.T, pool *pgxpool.Pool) *knowledge.Store {
	t.Helper()
	if err := database.Migrate(dbURL(t), "knowledge", os.DirFS("../knowledge/migrations")); err != nil {
		t.Fatalf("migrate knowledge: %v", err)
	}
	return knowledge.NewStore(pool)
}
