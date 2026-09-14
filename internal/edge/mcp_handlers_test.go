package edge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	"github.com/novaforge/novaforge/internal/edge"
)

// captureMcp records what the edge forwards, standing in for the mcp-server
// register, whose own behaviour is tested against real PostgreSQL and the
// real identity server in internal/mcp.
type captureMcp struct {
	mcpv1.McpServiceClient
	decide *mcpv1.DecideServerRequest
	revoke *mcpv1.RevokeServerRequest
}

func (c *captureMcp) DecideServer(_ context.Context, in *mcpv1.DecideServerRequest, _ ...grpc.CallOption) (*mcpv1.DecideServerResponse, error) {
	c.decide = in
	return &mcpv1.DecideServerResponse{Server: &mcpv1.McpServer{Id: in.GetId(), Status: "rejected"}}, nil
}

func (c *captureMcp) RevokeServer(_ context.Context, in *mcpv1.RevokeServerRequest, _ ...grpc.CallOption) (*mcpv1.RevokeServerResponse, error) {
	c.revoke = in
	return &mcpv1.RevokeServerResponse{Server: &mcpv1.McpServer{Id: in.GetId(), Status: "revoked"}}, nil
}

func mcpRouter(m *captureMcp) http.Handler {
	cfg := edge.Config{
		Identity: stubIdentity{subject: &identityv1.Subject{UserId: uuid.NewString(), OrgId: uuid.NewString(), ActorKind: "user"}},
		MCP:      m,
	}
	cfg.Handlers = edge.Handlers(cfg)
	return edge.NewRouter(cfg)
}

func send(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer nf_tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestMcpDecisionIsSpelledOut: the decision body names what is being done.
// A bare boolean would make an omitted field a rejection, and a typo in the
// word must be refused rather than read as either.
func TestMcpDecisionIsSpelledOut(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		body        string
		wantStatus  int
		wantApprove bool
	}{
		{`{"decision":"approve"}`, http.StatusOK, true},
		{`{"decision":"reject","reason":"no audit log"}`, http.StatusOK, false},
		{`{"decision":"aprove"}`, http.StatusBadRequest, false},
		{`{}`, http.StatusBadRequest, false},
	} {
		m := &captureMcp{}
		rec := send(mcpRouter(m), http.MethodPost, "/api/v1/orgs/acme/mcp/servers/"+id+"/decision", tc.body)
		if rec.Code != tc.wantStatus {
			t.Fatalf("%s: status %d %s", tc.body, rec.Code, rec.Body.String())
		}
		if tc.wantStatus != http.StatusOK {
			if m.decide != nil {
				t.Fatalf("%s: a refused body still reached the service", tc.body)
			}
			continue
		}
		if m.decide.GetId() != id || m.decide.GetApprove() != tc.wantApprove {
			t.Fatalf("%s: forwarded %+v", tc.body, m.decide)
		}
	}

	m := &captureMcp{}
	rec := send(mcpRouter(m), http.MethodDelete, "/api/v1/orgs/acme/mcp/servers/"+id+"?reason=vendor+breach", "")
	if rec.Code != http.StatusOK || m.revoke.GetId() != id || m.revoke.GetReason() != "vendor breach" {
		t.Fatalf("revoke: %d %s forwarded %+v", rec.Code, rec.Body.String(), m.revoke)
	}
}
