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

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/mcp"
	"github.com/novaforge/novaforge/internal/service"
)

// defaultHTTPPort is used when HTTP_PORT is not set in the environment.
const defaultHTTPPort = 8084

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
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = defaultHTTPPort
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

	be := newBackend(
		identityv1.NewIdentityServiceClient(identityConn),
		gitv1.NewGitServiceClient(gitConn),
		workv1.NewWorkServiceClient(workConn),
		reviewsv1.NewReviewsServiceClient(workConn),
		graphv1.NewGraphServiceClient(graphConn),
		gatesv1.NewGatesServiceClient(gatesConn),
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
	// permanently unready while the server itself was fine.
	go func() {
		if err := service.Serve(ctx, cfg, nil, func(context.Context) error { return nil }); err != nil {
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
