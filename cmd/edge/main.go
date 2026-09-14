// Command edge serves the NovaForge REST API. It terminates the public
// contract in api/openapi.yaml and fans out to the services over gRPC; it owns
// no data of its own.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/service"
)

func main() {
	cfg := service.LoadConfig()
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 8080
	}

	identityConn, err := dial(cfg.IdentityAddr)
	if err != nil {
		log.Fatalf("edge: dial identity: %v", err)
	}
	defer identityConn.Close()

	gitConn, err := dial(cfg.GitAddr)
	if err != nil {
		log.Fatalf("edge: dial git-platform: %v", err)
	}
	defer gitConn.Close()

	workConn, err := dial(cfg.WorkAddr)
	if err != nil {
		log.Fatalf("edge: dial work-reviews: %v", err)
	}
	defer workConn.Close()

	ciConn, err := dial(cfg.CIAddr)
	if err != nil {
		log.Fatalf("edge: dial ci-runner: %v", err)
	}
	defer ciConn.Close()

	// AGENTS_ADDR unset means this deployment runs no agents: the agent
	// routes are then absent rather than present and failing, which is what
	// the route table's "no client, no handler" rule gives us.
	var agentsClient agentsv1.AgentServiceClient
	if cfg.AgentsAddr != "" {
		agentsConn, err := dial(cfg.AgentsAddr)
		if err != nil {
			log.Fatalf("edge: dial agent-runtime: %v", err)
		}
		defer agentsConn.Close()
		agentsClient = agentsv1.NewAgentServiceClient(agentsConn)
	} else {
		log.Println("edge: AGENTS_ADDR is unset; the agent routes are not mounted")
	}

	// The graph backs the knowledge and symbol screens. A deployment without
	// it keeps every other route; those two report themselves unavailable,
	// which is what the "no client, no handler" rule gives us.
	var graphClient graphv1.GraphServiceClient
	if cfg.GraphAddr != "" {
		graphConn, err := dial(cfg.GraphAddr)
		if err != nil {
			log.Fatalf("edge: dial engineering-graph: %v", err)
		}
		defer graphConn.Close()
		graphClient = graphv1.NewGraphServiceClient(graphConn)
	} else {
		log.Println("edge: GRAPH_ADDR is unset; the knowledge and graph routes are not mounted")
	}

	var gatesClient gatesv1.GatesServiceClient
	if cfg.GatesAddr != "" {
		gatesConn, err := dial(cfg.GatesAddr)
		if err != nil {
			log.Fatalf("edge: dial gates: %v", err)
		}
		defer gatesConn.Close()
		gatesClient = gatesv1.NewGatesServiceClient(gatesConn)
	}

	// The chart has always set MCP_ADDR, and nothing read it. The MCP server
	// register routes now depend on it; unset, they are absent (501) rather
	// than answering as if no server had ever been requested.
	var mcpClient mcpv1.McpServiceClient
	if cfg.MCPAddr != "" {
		mcpConn, err := dial(cfg.MCPAddr)
		if err != nil {
			log.Fatalf("edge: dial mcp-server: %v", err)
		}
		defer mcpConn.Close()
		mcpClient = mcpv1.NewMcpServiceClient(mcpConn)
	} else {
		log.Println("edge: MCP_ADDR is unset; the MCP server register routes are not mounted")
	}

	ecfg := edge.Config{
		Identity: identityv1.NewIdentityServiceClient(identityConn),
		Git:      gitv1.NewGitServiceClient(gitConn),
		Work:     workv1.NewWorkServiceClient(workConn),
		Reviews:  reviewsv1.NewReviewsServiceClient(workConn),
		CI:       civ1.NewCIServiceClient(ciConn),
		Agents:   agentsClient,
		Graph:    graphClient,
		Gates:    gatesClient,
		MCP:      mcpClient,
	}
	ecfg.Handlers = edge.Handlers(ecfg)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           edge.NewRouter(ecfg),
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("edge: http listening on :%d", cfg.HTTPPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("edge: http server: %v", err)
		}
	}()

	// Readiness reflects the services the edge cannot work without. Reporting
	// ready while identity is unreachable would send traffic to a 500.
	check := func(ctx context.Context) error {
		for name, conn := range map[string]*grpc.ClientConn{
			"identity": identityConn, "git-platform": gitConn, "work-reviews": workConn,
		} {
			if s := conn.GetState().String(); s == "TRANSIENT_FAILURE" || s == "SHUTDOWN" {
				return fmt.Errorf("%s connection is %s", name, s)
			}
		}
		return nil
	}

	ctx := context.Background()
	if err := service.Serve(ctx, cfg, nil, check); err != nil {
		log.Fatalf("edge: %v", err)
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	log.Println("edge: stopped")
}

func dial(addr string) (*grpc.ClientConn, error) {
	if addr == "" {
		return nil, fmt.Errorf("no address configured")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	return grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Carry the caller's credential onto every downstream call; the edge is
		// a gateway, not a trusted principal of its own.
		grpc.WithChainUnaryInterceptor(edge.ForwardCredential),
	)
}
