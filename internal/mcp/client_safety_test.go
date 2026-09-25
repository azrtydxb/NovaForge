package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClientDoesNotFollowRedirect(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": ProtocolVersion}})
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	c := NewClient(ServerDef{Name: "approved", URL: redirect.URL, Token: "sensitive"}, []string{"approved"})
	if err := c.Connect(context.Background()); err == nil {
		t.Error("redirect was accepted")
	}
	if reached.Load() {
		t.Fatal("client contacted an unapproved redirect destination")
	}
}
func TestClientRejectsMismatchedJSONResponseID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 999, "result": map[string]any{"protocolVersion": ProtocolVersion}})
	}))
	defer srv.Close()
	c := NewClient(ServerDef{Name: "approved", URL: srv.URL}, []string{"approved"})
	if err := c.Connect(context.Background()); err == nil {
		t.Fatal("accepted response for another request")
	}
}

func TestExternalToolDiscoveryIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		definitions := make([]ToolDef, 300)
		for i := range definitions {
			definitions[i] = ToolDef{Name: "tool", InputSchema: map[string]any{"type": "object"}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": definitions}})
	}))
	defer srv.Close()
	c := NewClient(ServerDef{Name: "hostile", URL: srv.URL}, []string{"hostile"})
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("unbounded external tool definitions accepted")
	}
}
