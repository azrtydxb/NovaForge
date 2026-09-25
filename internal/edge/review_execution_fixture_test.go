package edge_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/reviews"
)

// Controlled managed owner, not an unrestricted SDK model double. The queue
// composition exercises actual SDK serialization and authenticated TLS receipts.
// Actual Fastllm/model qualification remains a separate acceptance requirement.
type queueReviewModel struct {
	calls            atomic.Int32
	fail             atomic.Bool
	mu               sync.Mutex
	entered, release chan struct{}
}

func (m *queueReviewModel) gateway(t *testing.T, store *reviews.Store) *reviews.ExecutionGateway {
	t.Helper()
	receipts := make(map[string]map[string]any)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer queue-principal" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/executions/v1/capabilities" {
			io.WriteString(w, `{"version":1,"execution_id_header":"X-Execution-Id","request_digest":"sha256-exact-body","replay":"never","protocols":["openai"],"stream":false,"structured_output":"one-forced-function-tool","status_path":"/executions/v1/{execution_id}","request_bytes":1048576,"response_bytes":8388608}`)
			return
		}
		if r.Method == http.MethodGet {
			id := strings.TrimPrefix(r.URL.Path, "/executions/v1/")
			m.mu.Lock()
			receipt, ok := receipts[id]
			m.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(receipt)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/executions/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		id := r.Header.Get("X-Execution-Id")
		if _, err := uuid.Parse(id); err != nil {
			http.Error(w, "execution id required", 400)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "body rejected", 400)
			return
		}
		var request struct {
			MaxTokens  int `json:"max_completion_tokens"`
			ToolChoice struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_choice"`
		}
		if json.Unmarshal(body, &request) != nil || request.MaxTokens != 256 || request.ToolChoice.Type != "function" || request.ToolChoice.Function.Name != "output" {
			t.Error("SDK output bound or forced output tool not forwarded")
			http.Error(w, "unsupported request", 400)
			return
		}
		digestBytes := sha256.Sum256(body)
		digest := hex.EncodeToString(digestBytes[:])
		created := time.Now().UTC().Format(time.RFC3339Nano)
		m.mu.Lock()
		if _, exists := receipts[id]; exists {
			m.mu.Unlock()
			http.Error(w, "no replay", 409)
			return
		}
		receipts[id] = map[string]any{"version": 1, "execution_id": id, "request_digest": digest, "phase": "dispatched", "terminal": false, "created_at": created}
		entered, release := m.entered, m.release
		m.mu.Unlock()
		m.calls.Add(1)
		if entered != nil {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return // no terminal receipt on cancellation
			}
		}
		m.mu.Lock()
		// Replace rather than mutate the map potentially being encoded by GET.
		receipts[id] = map[string]any{"version": 1, "execution_id": id, "request_digest": digest, "phase": "completed", "terminal": true, "upstream_status": 200, "created_at": created, "completed_at": time.Now().UTC().Format(time.RFC3339Nano)}
		m.mu.Unlock()
		w.Header().Set("X-Execution-Id", id)
		w.Header().Set("X-Execution-Digest", digest)
		w.Header().Set("X-Execution-Terminal", "true")
		json.NewEncoder(w).Encode(queueReviewCompletion(m.fail.Load()))
	}))
	t.Cleanup(server.Close)
	// This package has no parallel tests. Only construction sees the fixture CA;
	// the constructor clones its transport, and verification stays enabled.
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = original }()
	gateway, err := reviews.NewExecutionGateway(context.Background(), store, server.URL+"/executions/v1", "queue-principal", "review-model")
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}
