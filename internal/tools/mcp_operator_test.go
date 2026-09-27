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
	"google.golang.org/protobuf/proto"
)

func TestOperatorMCPBindingRotationAndRevocation(t *testing.T) {
	org, repo, id := uuid.New(), uuid.New(), uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "agent"})
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	binding := tools.MCPBinding{OrgID: org, RepoID: repo, ServerID: id, URL: "https://mcp.example.test/mcp", Transport: "streamable_http", TokenFile: "token", CIDRs: []string{"10.42.0.0/16"}}
	write := func(bindings ...tools.MCPBinding) {
		t.Helper()
		raw, err := json.Marshal(tools.OperatorMCP{Bindings: bindings})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	token := func(value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "token"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(binding)
	token("first")
	policy, err := tools.LoadOperatorMCP(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &mcpv1.McpServer{Id: id.String(), OrgId: org.String(), Url: binding.URL, Transport: binding.Transport}
	options := policy.Options(org, repo, nil)
	auth, err := options.HTTPAuthorization(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := auth.BearerToken(ctx); err != nil || got != "first" {
		t.Fatal("initial credential resolution failed")
	}
	token("second")
	if got, err := auth.BearerToken(ctx); err != nil || got != "second" {
		t.Fatal("rotation not observed")
	}
	other := authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New(), ActorKind: "agent"})
	if _, err := auth.BearerToken(other); err == nil {
		t.Fatal("cross-org credential read")
	}
	if _, err := policy.Options(org, uuid.New(), nil).HTTPAuthorization(ctx, server); err == nil {
		t.Fatal("cross-repo binding accepted")
	}
	changed := proto.Clone(server).(*mcpv1.McpServer)
	changed.OrgId = uuid.New().String()
	if _, err := options.HTTPAuthorization(ctx, changed); err == nil {
		t.Fatal("foreign server accepted")
	}
	changed = proto.Clone(server).(*mcpv1.McpServer)
	changed.Url = "https://other.example.test/mcp"
	if _, err := options.HTTPAuthorization(ctx, changed); err == nil {
		t.Fatal("destination substitution accepted")
	}
	if err := os.Remove(filepath.Join(dir, "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.BearerToken(ctx); err == nil {
		t.Fatal("deleted credential accepted")
	}
	token("second")
	write()
	if err := auth.Check(ctx); err == nil {
		t.Fatal("revoked policy accepted")
	}
	if _, err := auth.BearerToken(ctx); err == nil {
		t.Fatal("revoked policy exposed credential")
	}
	write(binding, binding)
	if _, err := tools.LoadOperatorMCP(path); err == nil {
		t.Fatal("ambiguous binding accepted")
	}
	binding.TokenFile = "../token"
	write(binding)
	if _, err := tools.LoadOperatorMCP(path); err == nil {
		t.Fatal("escaping token reference accepted")
	}
}
