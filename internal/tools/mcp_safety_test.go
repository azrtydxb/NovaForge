package tools_test

import (
	"context"
	"fmt"
	"github.com/novaforge/novaforge/internal/agents"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	"github.com/novaforge/novaforge/internal/tools"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type approvedServers struct{ server *mcpv1.McpServer }

func (a *approvedServers) ListApprovedServers(context.Context, *mcpv1.ListApprovedServersRequest, ...grpc.CallOption) (*mcpv1.ListApprovedServersResponse, error) {
	return &mcpv1.ListApprovedServersResponse{Servers: []*mcpv1.McpServer{a.server}}, nil
}

func TestMCPApprovalBindsExactDestination(t *testing.T) {
	endpoint, calls := externalMCPServer(t, "lookup", "data")
	servers := &approvedServers{server: &mcpv1.McpServer{Id: uuid.NewString(), Name: "safe", Url: endpoint, Transport: "streamable_http", Status: "approved"}}
	pool := auditPool(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newTestRun(t, ctx, agents.NewStore(pool), orgID)
	reg := tools.NewRegistry(tools.Runtime{}, agents.NewAuditLog(pool))
	offered, err := tools.OfferApprovedMCPServers(ctx, reg, servers)
	if err != nil {
		t.Fatal(err)
	}
	defer offered.Close()
	// Replacing the approved server under the same name must not preserve an
	// old run's authority to contact the old destination.
	replacement := proto.Clone(servers.server).(*mcpv1.McpServer)
	replacement.Id = uuid.NewString()
	servers.server = replacement
	if _, err := reg.Call(ctx, run.ID, "mcp.safe.lookup", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "no longer approved") {
		t.Fatal("replaced server remained callable")
	}
	if calls.Load() != 0 {
		t.Fatal("revoked destination was contacted")
	}
}
func TestMCPHTTPRequiresExplicitAuthorizationPolicy(t *testing.T) {
	endpoint, _ := externalMCPServer(t, "lookup", "data")
	servers := &approvedServers{server: &mcpv1.McpServer{Id: uuid.NewString(), Name: "safe", Url: endpoint, Transport: "streamable_http", Status: "approved"}}
	for _, opts := range []tools.MCPOptions{{}, {HTTPAuthorization: func(context.Context, *mcpv1.McpServer) (tools.MCPAuthorization, error) {
		return tools.MCPAuthorization{}, nil
	}}, {HTTPAuthorization: func(context.Context, *mcpv1.McpServer) (tools.MCPAuthorization, error) {
		return tools.MCPAuthorization{BearerToken: func(context.Context) (string, error) { return "", fmt.Errorf("missing secret") }}, nil
	}}} {
		reg := tools.NewRegistry(tools.Runtime{}, nil)
		offered, err := tools.OfferApprovedMCPServersWithOptions(context.Background(), reg, servers, opts)
		defer offered.Close()
		if err != nil || len(offered.Tools) != 0 || len(offered.Skipped) != 1 {
			t.Fatalf("not fail closed: %+v, %v", offered, err)
		}
	}
}

type noLifecycleStream struct{ closes int }

func (s *noLifecycleStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *noLifecycleStream) Write(b []byte) (int, error) { return len(b), nil }
func (s *noLifecycleStream) Close() error                { s.closes++; return fmt.Errorf("unconfirmed teardown") }
func TestMCPStdioRequiresIndependentLifecycleSignal(t *testing.T) {
	stream := &noLifecycleStream{}
	cancelled := false
	servers := &approvedServers{server: &mcpv1.McpServer{Id: uuid.NewString(), Name: "unsafe", Url: "command", Transport: "stdio", Status: "approved"}}
	reg := tools.NewRegistry(tools.Runtime{}, nil)
	offered, err := tools.OfferApprovedMCPServersWithOptions(context.Background(), reg, servers, tools.MCPOptions{AllowedTools: []string{"mcp.unsafe.lookup"}, StdioClosing: func() { cancelled = true }, OpenStdio: func(context.Context, string) (io.ReadWriteCloser, error) { return stream, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if len(offered.Tools) != 0 || !cancelled || stream.closes != 1 {
		t.Fatalf("io-only transport accepted or abandoned: %+v cancelled=%v closes=%d", offered, cancelled, stream.closes)
	}
	if err = offered.Close(); err == nil {
		t.Fatal("unconfirmed io-only teardown forgotten")
	}
}

type failedDiscoveryStream struct{ noLifecycleStream }

func (*failedDiscoveryStream) OnExit(func()) {}
func TestMCPRetainsFailedDiscoveryAndPartialOpen(t *testing.T) {
	for _, openFailed := range []bool{false, true} {
		t.Run(fmt.Sprint(openFailed), func(t *testing.T) {
			stream := &failedDiscoveryStream{}
			servers := &approvedServers{server: &mcpv1.McpServer{Id: uuid.NewString(), Name: "failed", Url: "command", Transport: "stdio", Status: "approved"}}
			offered, err := tools.OfferApprovedMCPServersWithOptions(context.Background(), tools.NewRegistry(tools.Runtime{}, nil), servers, tools.MCPOptions{AllowedTools: []string{"mcp.failed.lookup"}, OpenStdio: func(context.Context, string) (io.ReadWriteCloser, error) {
				if openFailed {
					return stream, fmt.Errorf("opened but initialization failed")
				}
				return stream, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(offered.Tools) != 0 || stream.closes != 1 {
				t.Fatalf("failed stream not closed: %+v closes=%d", offered, stream.closes)
			}
			if err = offered.Close(); err == nil {
				t.Fatal("failed discovery/open lost its teardown responsibility")
			}
		})
	}
}
