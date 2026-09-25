package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/tools"
)

func TestMCPHTTPPolicyBindsOrganizationServerAndDestination(t *testing.T) {
	org, server := uuid.New(), uuid.New()
	dir := t.TempDir()
	secret := filepath.Join(dir, "token")
	if err := os.WriteFile(secret, []byte("first-fixture-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	policy := tools.MCPHTTPPolicy{OrgID: org.String(), ServerID: server.String(), URL: "https://mcp.example/mcp", BearerTokenFile: secret}
	body, _ := json.Marshal([]tools.MCPHTTPPolicy{policy})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	authorize, err := tools.LoadMCPHTTPPolicies(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "agent"})
	srv := &mcpv1.McpServer{Id: server.String(), OrgId: org.String(), Url: policy.URL, Status: "approved", Transport: "streamable_http"}
	a, err := authorize(ctx, srv)
	if err != nil || a.Public || a.BearerToken == nil {
		t.Fatalf("static auth: %+v %v", a, err)
	}
	if got, err := a.BearerToken(ctx); err != nil || got != "first-fixture-token" {
		t.Fatalf("token: %v", err)
	}
	if err := os.WriteFile(secret, []byte("rotated-fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := a.BearerToken(ctx); err != nil || got != "rotated-fixture-token" {
		t.Fatalf("rotation: %v", err)
	}
	for _, change := range []func(*mcpv1.McpServer){func(s *mcpv1.McpServer) { s.OrgId = uuid.NewString() }, func(s *mcpv1.McpServer) { s.Id = uuid.NewString() }, func(s *mcpv1.McpServer) { s.Url += "/other" }, func(s *mcpv1.McpServer) { s.Status = "revoked" }, func(s *mcpv1.McpServer) { s.Transport = "stdio" }} {
		candidate := &mcpv1.McpServer{Id: srv.Id, OrgId: srv.OrgId, Url: srv.Url, Status: srv.Status, Transport: srv.Transport}
		change(candidate)
		if _, err = authorize(ctx, candidate); err == nil {
			t.Fatalf("accepted changed policy binding: %+v", candidate)
		}
	}
	foreign := authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New(), ActorKind: "agent"})
	if _, err = authorize(foreign, srv); err == nil {
		t.Fatal("foreign organization authorized")
	}
	if err = os.Remove(secret); err != nil {
		t.Fatal(err)
	}
	if _, err = a.BearerToken(ctx); err == nil {
		t.Fatal("missing static file accepted")
	}
}

func TestMCPHTTPPolicyRejectsAmbiguousOperatorConfig(t *testing.T) {
	for _, raw := range []string{"http://public.example/mcp", "https://user:secret@mcp.example/mcp", "https://mcp.example/mcp?token=x", "https://mcp.example/mcp#fragment", "HTTPS://mcp.example/mcp", "https://MCP.example/mcp"} {
		t.Run(raw, func(t *testing.T) {
			p := tools.MCPHTTPPolicy{OrgID: uuid.NewString(), ServerID: uuid.NewString(), URL: raw, Public: true}
			body, _ := json.Marshal([]tools.MCPHTTPPolicy{p})
			path := filepath.Join(t.TempDir(), "policy")
			os.WriteFile(path, body, 0600)
			if _, err := tools.LoadMCPHTTPPolicies(path); err == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
	for _, raw := range []string{"https://mcp.example/mcp", "http://127.0.0.1:8888/mcp", "http://mcp.novaforge.svc/mcp"} {
		p := tools.MCPHTTPPolicy{OrgID: uuid.NewString(), ServerID: uuid.NewString(), URL: raw, Public: true}
		body, _ := json.Marshal([]tools.MCPHTTPPolicy{p})
		path := filepath.Join(t.TempDir(), "policy")
		os.WriteFile(path, body, 0600)
		if _, err := tools.LoadMCPHTTPPolicies(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCPHTTPPolicyRejectsNonCanonicalJSON(t *testing.T) {
	prefix := `[{"org_id":"` + uuid.NewString() + `","server_id":"` + uuid.NewString() + `","url":"https://mcp.example/mcp",`
	for _, suffix := range []string{
		`"public":false,"public":true}]`,
		`"Public":true}]`,
		`"public":true,"bearer_token_file":null}]`,
		`"public":true,"unknown":false}]`,
	} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(path, []byte(prefix+suffix), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := tools.LoadMCPHTTPPolicies(path); err == nil {
				t.Fatal("ambiguous operator authority accepted")
			}
		})
	}
}
