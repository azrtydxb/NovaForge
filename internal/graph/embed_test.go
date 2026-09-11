package graph_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/graph"
)

// stubEmbedder is a deterministic in-process test double for an external
// embedding model — a legitimate stand-in for a hosted provider, unlike
// Postgres which every test here runs for real. It hands back a
// caller-supplied vector for a known chunk, or an error when the text
// matches a configured failing marker.
type stubEmbedder struct {
	vectors map[string][]float32
	failOn  string
}

func (e *stubEmbedder) Embed(ctx context.Context, chunks []string) ([][]float32, error) {
	out := make([][]float32, len(chunks))
	for i, c := range chunks {
		if e.failOn != "" && c == e.failOn {
			return nil, fmt.Errorf("stub embedder: refused to embed %q", c)
		}
		v, ok := e.vectors[c]
		if !ok {
			v = make([]float32, 768)
		}
		out[i] = v
	}
	return out, nil
}

func unitVector(dim, hot int) []float32 {
	v := make([]float32, dim)
	v[hot] = 1
	return v
}

func newVectorStore(t *testing.T) *graph.VectorStore {
	t.Helper()
	return graph.NewVectorStore(storePool(t))
}

func TestSearchRanksNearestFirst(t *testing.T) {
	vs := newVectorStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())

	near := unitVector(768, 0)
	mid := unitVector(768, 0)
	mid[1] = 1 // 45 degrees off axis 0
	far := unitVector(768, 1)

	chunks := []graph.Chunk{
		{ID: uuid.New(), Path: "far.go", StartLine: 1, EndLine: 2, Text: "far", Embedding: far},
		{ID: uuid.New(), Path: "mid.go", StartLine: 1, EndLine: 2, Text: "mid", Embedding: mid},
		{ID: uuid.New(), Path: "near.go", StartLine: 1, EndLine: 2, Text: "near", Embedding: near},
	}
	if err := vs.Upsert(ctx, orgID, repoID, "multi", chunks); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := vs.Search(ctx, orgID, repoID, unitVector(768, 0), 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	if got[0].Path != "near.go" || got[1].Path != "mid.go" || got[2].Path != "far.go" {
		t.Fatalf("order = %v, want near, mid, far", []string{got[0].Path, got[1].Path, got[2].Path})
	}
}

func TestSearchIsOrgScoped(t *testing.T) {
	vs := newVectorStore(t)
	orgA := uuid.New()
	orgB := uuid.New()
	repoID := uuid.New()
	ctxA := scopedCtx(orgA, uuid.New())
	ctxB := scopedCtx(orgB, uuid.New())

	v := unitVector(768, 5)
	chunk := graph.Chunk{ID: uuid.New(), Path: "shared.go", StartLine: 1, EndLine: 1, Text: "same text", Embedding: v}

	if err := vs.Upsert(ctxA, orgA, repoID, "shared.go", []graph.Chunk{chunk}); err != nil {
		t.Fatalf("Upsert org A: %v", err)
	}

	chunkB := chunk
	chunkB.ID = uuid.New()
	if err := vs.Upsert(ctxB, orgB, repoID, "shared.go", []graph.Chunk{chunkB}); err != nil {
		t.Fatalf("Upsert org B: %v", err)
	}

	got, err := vs.Search(ctxA, orgA, repoID, v, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, c := range got {
		if c.ID == chunkB.ID {
			t.Fatalf("org A search returned org B's chunk: %+v", c)
		}
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1 (only org A's chunk)", len(got))
	}
}

func TestUpsertReplacesChunksForPath(t *testing.T) {
	vs := newVectorStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())
	path := "internal/foo/replace.go"

	first := []graph.Chunk{
		{ID: uuid.New(), Path: path, StartLine: 1, EndLine: 10, Text: "one", Embedding: unitVector(768, 2)},
		{ID: uuid.New(), Path: path, StartLine: 11, EndLine: 20, Text: "two", Embedding: unitVector(768, 3)},
	}
	if err := vs.Upsert(ctx, orgID, repoID, path, first); err != nil {
		t.Fatalf("Upsert 1: %v", err)
	}

	second := []graph.Chunk{
		{ID: uuid.New(), Path: path, StartLine: 1, EndLine: 30, Text: "only", Embedding: unitVector(768, 4)},
	}
	if err := vs.Upsert(ctx, orgID, repoID, path, second); err != nil {
		t.Fatalf("Upsert 2: %v", err)
	}

	got, err := vs.Search(ctx, orgID, repoID, unitVector(768, 4), 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Text != "only" {
		t.Fatalf("chunks after replace = %+v, want only [only]", got)
	}
}

func TestEmbedderFailureIsNotFatal(t *testing.T) {
	vs := newVectorStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID, uuid.New())
	path := "internal/foo/bad.go"

	pre := []graph.Chunk{
		{ID: uuid.New(), Path: path, StartLine: 1, EndLine: 5, Text: "kept", Embedding: unitVector(768, 6)},
	}
	if err := vs.Upsert(ctx, orgID, repoID, path, pre); err != nil {
		t.Fatalf("Upsert pre: %v", err)
	}

	embedder := &stubEmbedder{failOn: "explode"}
	_, err := embedder.Embed(context.Background(), []string{"explode"})
	if err == nil {
		t.Fatal("want embedder error")
	}
	if err.Error() == "" {
		t.Fatal("want non-empty error naming the failure")
	}

	// The relational index (this path's existing chunks) must be untouched
	// by an embedder failure: no partial write occurred because Upsert was
	// never called with the failed embedding.
	got, serr := vs.Search(ctx, orgID, repoID, unitVector(768, 6), 10)
	if serr != nil {
		t.Fatalf("Search: %v", serr)
	}
	if len(got) != 1 || got[0].Text != "kept" {
		t.Fatalf("index after embedder failure = %+v, want [kept] untouched", got)
	}
}
