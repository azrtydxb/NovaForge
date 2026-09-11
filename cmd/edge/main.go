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

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
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

	ecfg := edge.Config{
		Identity: identityv1.NewIdentityServiceClient(identityConn),
		Git:      gitv1.NewGitServiceClient(gitConn),
		Work:     workv1.NewWorkServiceClient(workConn),
		Reviews:  reviewsv1.NewReviewsServiceClient(workConn),
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
