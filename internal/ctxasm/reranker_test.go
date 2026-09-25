package ctxasm

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

type invalidRanker struct{}

func (invalidRanker) Rank(context.Context, string, []string) ([]float32, error) {
	return []float32{float32(math.NaN()), float32(math.Inf(1))}, nil
}
func TestRerankRejectsNonFiniteScores(t *testing.T) {
	got := rerank(t.Context(), invalidRanker{}, "query", []string{"a", "b"})
	if got[0] != 2 || got[1] != 1 {
		t.Fatalf("invalid scores retained: %v", got)
	}
}

func TestGatewayRerankerUsesSDKAndValidatesScores(t *testing.T) {
	response := `{"scores":[0.1,0.9]}`
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("gateway request %s", r.URL.Path)
		}
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if string(req["model"]) != `"local/ranker"` || len(req["tools"]) == 0 {
			t.Error("SDK structured call missing")
		}
		encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "score", "type": "function", "function": map[string]any{"name": "output", "arguments": response}}}}}}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(encoded)
	}))
	defer srv.Close()
	r, err := NewGatewayReranker(srv.URL, "local/ranker", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Rank(t.Context(), "query", []string{"a", "b"})
	if err != nil || len(got) != 2 || got[1] != 0.9 {
		t.Fatalf("rank %v %v", got, err)
	}
	for _, bad := range []string{`{"scores":[1]}`, `{"scores":[-1,2]}`, `{"scores":[null,0.2]}`} {
		response = bad
		if _, err := r.Rank(t.Context(), "query", []string{"a", "b"}); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	if calls != 4 {
		t.Fatalf("calls %d", calls)
	}
	for _, endpoint := range []string{"", "ftp://example.com", "https://key@example.com"} {
		if _, err := NewGatewayReranker(endpoint, "model", ""); err == nil {
			t.Fatal("bad endpoint accepted")
		}
	}
}
