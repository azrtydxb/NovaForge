// Command mcp-server runs the mcp-server service: NovaForge's own MCP
// server, exposing the platform to external agents (Claude Code, Codex, and
// similar) over stdio or Streamable HTTP.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/cleanup"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/mcp"
	"github.com/novaforge/novaforge/internal/service"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// defaultHTTPPort is used when HTTP_PORT is not set in the environment.
const defaultHTTPPort = 8084

// defaultGRPCPort matches services.mcp-server.grpcPort in the chart.
const defaultGRPCPort = 9098

func main() {
	cfg := service.LoadConfig()
	if cfg.IdentityAddr == "" {
		log.Fatal("mcp-server: IDENTITY_ADDR is required")
	}
	if cfg.GitAddr == "" {
		log.Fatal("mcp-server: GIT_ADDR is required")
	}
	if cfg.WorkAddr == "" {
		log.Fatal("mcp-server: WORK_ADDR is required")
	}
	if cfg.GatesAddr == "" {
		log.Fatal("mcp-server: GATES_ADDR is required")
	}
	if cfg.GraphAddr == "" {
		log.Fatal("mcp-server: GRAPH_ADDR is required")
	}
	if cfg.CIAddr == "" {
		log.Fatal("mcp-server: CI_ADDR is required")
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = defaultHTTPPort
	}
	if cfg.GRPCPort == 0 {
		cfg.GRPCPort = defaultGRPCPort
	}
	// The register of approved external MCP servers is this service's own
	// data, in its own schema. Without a database the register cannot answer,
	// and a service that starts anyway looks like it has no servers approved.
	if cfg.DatabaseURL == "" {
		log.Fatal("mcp-server: DATABASE_URL is required")
	}
	if err := database.Migrate(cfg.DatabaseURL, "mcp", mcp.MigrationsFS); err != nil {
		log.Fatalf("mcp-server: migrate mcp schema: %v", err)
	}

	dial := func(name, addr string) *grpc.ClientConn {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("mcp-server: dial %s: %v", name, err)
		}
		return conn
	}

	identityConn := dial("identity", cfg.IdentityAddr)
	defer identityConn.Close()
	gitConn := dial("git-platform", cfg.GitAddr)
	defer gitConn.Close()
	workConn := dial("work-reviews", cfg.WorkAddr)
	defer workConn.Close()
	gatesConn := dial("gates", cfg.GatesAddr)
	defer gatesConn.Close()
	graphConn := dial("engineering-graph", cfg.GraphAddr)
	defer graphConn.Close()
	ciConn := dial("ci-runner", cfg.CIAddr)
	defer ciConn.Close()

	be := newBackend(
		identityv1.NewIdentityServiceClient(identityConn),
		gitv1.NewGitServiceClient(gitConn),
		workv1.NewWorkServiceClient(workConn),
		reviewsv1.NewReviewsServiceClient(workConn),
		graphv1.NewGraphServiceClient(graphConn),
		gatesv1.NewGatesServiceClient(gatesConn),
		civ1.NewCIServiceClient(ciConn),
	)
	mcpServer := mcp.NewServer(be)
	if token := os.Getenv("MCP_TOKEN"); token != "" {
		// A stdio session has no per-request header to carry a credential;
		// MCP_TOKEN lets an operator configure one static "<org>:<credential>"
		// token for a stdio-launched session (see backend.go's doc on the
		// token format).
		mcpServer.SetToken(token)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("mcp-server: connect database: %v", err)
	}
	defer pool.Close()

	// The register's role check calls identity as the person asking, so its
	// identity client forwards the incoming credential. It is a separate
	// connection from the MCP backend's, which builds its own outgoing
	// credentials per tool call and must not have a second one appended.
	registryIdentityConn, err := grpc.NewClient(cfg.IdentityAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		log.Fatalf("mcp-server: dial identity for the server register: %v", err)
	}
	defer registryIdentityConn.Close()
	registryIdentity := identityv1.NewIdentityServiceClient(registryIdentityConn)

	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(
		svcauth.UnaryServerInterceptor(registryIdentity, cfg.HMACSecret)))
	registry := mcp.NewRegistry(pool, registryIdentity)
	mcpv1.RegisterMcpServiceServer(grpcSrv, registry)

	// A deleted organization's registered servers go with it.
	eventBus, err := cleanup.Redis(cfg.RedisURL)
	if err != nil {
		log.Fatalf("mcp-server: %v", err)
	}
	defer eventBus.Close()
	cleanup.MCPServer(registry).Run(ctx, eventBus, "mcp-server")

	errCh := make(chan error, 2)

	// stdio is served only on an explicit opt-in. In a pod stdin is never a
	// real MCP client, and sniffing for a pipe made the process treat /dev/null
	// as a closed session and exit.
	if os.Getenv("NOVAFORGE_MCP_STDIO") == "1" {
		go func() {
			log.Printf("mcp-server: serving MCP over stdio")
			if err := mcpServer.ServeStdio(ctx, os.Stdin, os.Stdout); err != nil {
				errCh <- fmt.Errorf("stdio transport: %w", err)
				return
			}
			// A closed stdin (the parent process exited) ends the session;
			// there is nothing left for this process to do.
			errCh <- nil
		}()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpServer.ServeHTTP)
	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("mcp-server: serving Streamable HTTP on :%d/mcp", cfg.HTTPPort)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http transport: %w", err)
		}
	}()

	// Readiness lives on HEALTH_PORT like every other service, because that is
	// the port the chart probes; serving it only on the MCP port left the pod
	// permanently unready while the server itself was fine. The same call
	// serves the register's gRPC API on GRPC_PORT.
	go func() {
		check := func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("database: %w", err)
			}
			return nil
		}
		if err := service.Serve(ctx, cfg, grpcSrv, check); err != nil {
			errCh <- fmt.Errorf("health: %w", err)
		}
	}()

	if err := <-errCh; err != nil {
		log.Fatalf("mcp-server: %v", err)
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
}

// stdinIsPipe reports whether stdin is not a TTY — the platform's own
// signal that a parent process (an MCP client) has connected a pipe to
// speak the stdio transport over, as opposed to a terminal or /dev/null.
func stdinIsPipe() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) == 0
}
