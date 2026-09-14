package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/graph"
)

// servedModelWidth is the width of the embedding model this platform's
// gateway actually serves (bge-m3). The server below stands in for that
// gateway: an external model is the one thing a test here may double.
const servedModelWidth = 1024

// fakeEmbeddingGateway answers the OpenAI embeddings wire format with vectors
// of width dims, refuses any request without the expected bearer credential
// (as the cluster's gateway does), and records the credentials it was sent.
func fakeEmbeddingGateway(t *testing.T, wantKey string, dims int) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wantKey {
			http.Error(w, `{"error":{"message":"missing or invalid credential"}}`, http.StatusUnauthorized)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		out := struct {
			Data []datum `json:"data"`
		}{}
		for i := range req.Input {
			v := make([]float64, dims)
			v[i%dims] = 1
			out.Data = append(out.Data, datum{Index: i, Embedding: v})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), auths...)
	}
}

// TestEmbeddingsFromTheServedModelAreStorable drives the deployment's own
// embedder configuration — only AI_API_KEY set, as the chart sets it — against
// a gateway of the served model's width, and stores what comes back.
//
// Both halves were broken on the cluster: the embedder sent no credential
// (it read only EMBED_API_KEY, which nothing sets), and the schema stored 768
// dimensions while the model answers with 1024, so Upsert refused every chunk.
// Each failure was logged per file and indistinguishable from an idle index.
func TestEmbeddingsFromTheServedModelAreStorable(t *testing.T) {
	vs := newVectorStore(t)
	srv, auths := fakeEmbeddingGateway(t, "gateway-credential", servedModelWidth)

	t.Setenv("EMBED_ENDPOINT", srv.URL)
	t.Setenv("EMBED_MODEL", "embed")
	t.Setenv("EMBED_API_KEY", "")
	t.Setenv("AI_API_KEY", "gateway-credential")

	embedder, err := graph.NewEndpointEmbedder()
	if err != nil {
		t.Fatalf("NewEndpointEmbedder: %v", err)
	}
	vecs, err := embedder.Embed(context.Background(), []string{"func VATTotal() {}", "func ParseConfig() {}"})
	if err != nil {
		t.Fatalf("Embed (credentials sent: %q): %v", auths(), err)
	}
	if len(vecs) != 2 {
		t.Fatalf("Embed returned %d vectors, want 2", len(vecs))
	}

	orgID, repoID := uuid.New(), uuid.New()
	ctx := scopedCtx(orgID, uuid.New())
	chunks := []graph.Chunk{
		{Path: "vat.go", StartLine: 1, EndLine: 1, Text: "func VATTotal() {}", Embedding: vecs[0]},
		{Path: "vat.go", StartLine: 2, EndLine: 2, Text: "func ParseConfig() {}", Embedding: vecs[1]},
	}
	if err := vs.Upsert(ctx, orgID, repoID, "vat.go", chunks); err != nil {
		t.Fatalf("Upsert of the served model's embeddings: %v", err)
	}
	got, err := vs.Search(ctx, orgID, repoID, vecs[0], 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Text != "func VATTotal() {}" {
		t.Fatalf("Search = %+v, want the VATTotal chunk first", got)
	}
}

// TestVerifyEmbedderRefusesAModelOfTheWrongWidth checks the startup probe: a
// model whose vectors the schema cannot store is reported as such, rather than
// discovered one refused chunk at a time.
func TestVerifyEmbedderRefusesAModelOfTheWrongWidth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dims    int
		wantErr bool
	}{
		{"served width", servedModelWidth, false},
		{"narrower model", 768, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeEmbeddingGateway(t, "k", tc.dims)
			t.Setenv("EMBED_ENDPOINT", srv.URL)
			t.Setenv("EMBED_MODEL", "embed")
			t.Setenv("EMBED_API_KEY", "k")

			embedder, err := graph.NewEndpointEmbedder()
			if err != nil {
				t.Fatalf("NewEndpointEmbedder: %v", err)
			}
			err = graph.VerifyEmbedder(context.Background(), embedder)
			if tc.wantErr {
				if !errors.Is(err, graph.ErrEmbeddingWidth) {
					t.Fatalf("VerifyEmbedder = %v, want ErrEmbeddingWidth", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyEmbedder: %v", err)
			}
		})
	}
}
