// Command ci-runner runs the ci-runner service: the RunnerService gRPC
// surface runners register and connect to, the push-event scheduler that
// turns a push into a Run and Jobs, and the retention sweeper that deletes
// expired sealed logs and artifacts.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/retention"
	"github.com/novaforge/novaforge/internal/service"
)

// defaultGRPCPort is used when GRPC_PORT is not set in the environment.
const defaultGRPCPort = 9094

// artifactsBucket is the fixed bucket ci-runner stores sealed logs and
// artifacts in.
const artifactsBucket = "novaforge-ci"

func main() {
	cfg := service.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("ci-runner: DATABASE_URL is required")
	}
	if cfg.RedisURL == "" {
		log.Fatal("ci-runner: REDIS_URL is required")
	}
	if cfg.GitAddr == "" {
		log.Fatal("ci-runner: GIT_ADDR is required")
	}
	if cfg.S3Endpoint == "" {
		log.Fatal("ci-runner: S3_ENDPOINT is required")
	}
	if cfg.GRPCPort == 0 {
		cfg.GRPCPort = defaultGRPCPort
	}

	if err := database.Migrate(cfg.DatabaseURL, "ci", ci.MigrationsFS); err != nil {
		log.Fatalf("ci-runner: migrate ci schema: %v", err)
	}
	if err := database.Migrate(cfg.DatabaseURL, "retention", retention.MigrationsFS); err != nil {
		log.Fatalf("ci-runner: migrate retention schema: %v", err)
	}

	ctx := context.Background()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("ci-runner: connect database: %v", err)
	}
	defer pool.Close()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("ci-runner: parse redis url: %v", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	blobs, err := blobstore.New(ctx, blobstore.Options{
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		Bucket:    artifactsBucket,
	})
	if err != nil {
		log.Fatalf("ci-runner: connect object storage: %v", err)
	}

	gitConn, err := grpc.NewClient(cfg.GitAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("ci-runner: dial git-platform: %v", err)
	}
	defer gitConn.Close()
	gitClient := gitv1.NewGitServiceClient(gitConn)

	svc := ci.NewService(pool, rdb, blobs, gitClient, cfg.HMACSecret)

	// RunnerService has no authz.Scope to resolve: a runner authenticates
	// with the bearer token Register returned, verified inside Server's own
	// RPC handlers rather than by an interceptor. logInterceptor is passed
	// explicitly (rather than calling grpc.NewServer with no options) so
	// every RPC's outcome is logged the same way across every service
	// binary in this codebase.
	srv := grpc.NewServer(grpc.UnaryInterceptor(logInterceptor))
	civ1.RegisterRunnerServiceServer(srv, svc.Server)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	svc.Run(runCtx)

	check := func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		return nil
	}

	if err := service.Serve(ctx, cfg, srv, check); err != nil {
		log.Fatalf("ci-runner: serve: %v", err)
	}
}

// logInterceptor logs each RPC's method and outcome. RunnerService needs no
// authz.Scope (a runner is not acting as an organization member; its
// Register-issued token is verified inside Server's own handlers), so this
// is what every RPC passes through instead.
func logInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	resp, err := handler(ctx, req)
	if err != nil {
		log.Printf("ci-runner: %s: %v", info.FullMethod, err)
	}
	return resp, err
}
