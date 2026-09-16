// Command ci-runner runs the ci-runner service: the RunnerService gRPC
// surface runners register and connect to, the push-event scheduler that
// turns a push into a Run and Jobs, and the retention sweeper that deletes
// expired sealed logs and artifacts.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/cleanup"
	"github.com/novaforge/novaforge/internal/svcauth"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
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
	// Agent jobs run as Agent Runs briefed through Work Items. Without these
	// peers every agent job would sit pending forever — the silence that hid
	// agent jobs never running at all — so their absence is fatal instead.
	if cfg.AgentsAddr == "" || cfg.WorkAddr == "" {
		log.Fatal("ci-runner: AGENTS_ADDR and WORK_ADDR are required to execute agent jobs")
	}
	// A job that declares a secret is brokered its credentials by the gates
	// service at dispatch. Without the broker such a job could only fail, so
	// a deployment missing it is refused at start rather than discovered job
	// by job.
	if cfg.GatesAddr == "" {
		log.Fatal("ci-runner: GATES_ADDR is required to broker job credentials")
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

	// NewClient performs no I/O; connection failures surface on RPCs, whose
	// contexts must carry the appropriate deadline, not a dial-timeout option.
	gitConn, err := grpc.NewClient(cfg.GitAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("ci-runner: dial git-platform: %v", err)
	}
	defer gitConn.Close()
	gitClient := gitv1.NewGitServiceClient(gitConn)

	svc := ci.NewService(pool, rdb, blobs, gitClient, cfg.HMACSecret, env("GIT_CLONE_BASE", "http://novaforge-git-platform:8081"))

	// RunnerService has no authz.Scope to resolve: a runner authenticates
	// with the bearer token Register returned, verified inside Server's own
	// RPC handlers rather than by an interceptor. logInterceptor is passed
	// explicitly (rather than calling grpc.NewServer with no options) so
	// every RPC's outcome is logged the same way across every service
	// binary in this codebase.
	// The query surface serves people, so it needs the caller's scope; the
	// runner surface serves runners and resolves them separately.
	identityConn, err := grpc.NewClient(cfg.IdentityAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("ci-runner: dial identity: %v", err)
	}
	defer identityConn.Close()

	identityClient := identityv1.NewIdentityServiceClient(identityConn)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			logInterceptor,
			svcauth.UnaryServerInterceptor(identityClient, cfg.HMACSecret),
		),
		// Artifact downloads stream. Without the stream interceptor they reach
		// the query server with no caller and every download is refused. The
		// runner's Connect stream needs no scope and ignores the one this sets.
		grpc.ChainStreamInterceptor(svcauth.StreamServerInterceptor(identityClient, cfg.HMACSecret)),
	)
	civ1.RegisterRunnerServiceServer(srv, svc.Server)
	civ1.RegisterCIServiceServer(srv, svc.Query)

	agentsConn, err := grpc.NewClient(cfg.AgentsAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("ci-runner: dial agent-runtime: %v", err)
	}
	defer agentsConn.Close()
	workConn, err := grpc.NewClient(cfg.WorkAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("ci-runner: dial work-reviews: %v", err)
	}
	defer workConn.Close()

	gatesConn, err := grpc.NewClient(cfg.GatesAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("ci-runner: dial gates: %v", err)
	}
	defer gatesConn.Close()
	svc.Pump.Credentials = &ci.GatesBroker{
		Gates:      gatesv1.NewGatesServiceClient(gatesConn),
		HMACSecret: cfg.HMACSecret,
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	svc.Run(runCtx)
	// A deleted repository's or organization's CI — rows, artifact objects and
	// logs — is removed when the deletion is announced.
	cleanup.CI(&ci.Purger{Pool: pool, Logs: svc.Logs, Blobs: blobs}).Run(runCtx, rdb, "ci-runner")
	agentJobs := &ci.AgentJobs{
		Store:      svc.Store,
		Agents:     agentsv1.NewAgentServiceClient(agentsConn),
		Work:       workv1.NewWorkServiceClient(workConn),
		HMACSecret: cfg.HMACSecret,
	}
	go agentJobs.Run(runCtx)

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

// env returns the environment value for k, or def when it is unset.
func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
