// Command identity runs the identity gRPC service: users, organizations,
// sessions, personal access tokens, SSH keys, and capability grants.
package main

import (
	"context"
	"log"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/identity"
	"github.com/novaforge/novaforge/internal/service"
)

// defaultGRPCPort is used when GRPC_PORT is not set in the environment.
const defaultGRPCPort = 9091

func main() {
	cfg := service.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("identity: DATABASE_URL is required")
	}
	if cfg.RedisURL == "" {
		log.Fatal("identity: REDIS_URL is required")
	}
	if cfg.GRPCPort == 0 {
		cfg.GRPCPort = defaultGRPCPort
	}

	if err := database.Migrate(cfg.DatabaseURL, "identity", identity.MigrationsFS); err != nil {
		log.Fatalf("identity: migrate identity schema: %v", err)
	}
	// capability_grants lives in the gitplatform schema (see
	// internal/capability/grant.go); the identity service is what issues
	// and resolves grants via IssueGrant/GetGrant, so it owns applying this
	// migration.
	if err := database.Migrate(cfg.DatabaseURL, "gitplatform", capability.MigrationsFS); err != nil {
		log.Fatalf("identity: migrate gitplatform (capability) schema: %v", err)
	}

	ctx := context.Background()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("identity: connect database: %v", err)
	}
	defer pool.Close()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("identity: parse redis url: %v", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	store := identity.NewStore(pool)
	sessions := identity.NewSessionStore(rdb)
	tokens := identity.NewTokenStore(pool)
	sshKeys := identity.NewSSHKeyStore(pool)
	grants := capability.NewStore(pool)

	grpcServer := identity.NewGRPCServer(store, sessions, tokens, sshKeys, grants)
	// Issuing a run's grant is delegated: agent-runtime asks Identity, and
	// Identity validates the intent against what the run owner itself recorded
	// before issuing. That validation calls back into agent-runtime, so it needs
	// a client and a credential to authenticate with. Neither was set, so every
	// delegated issuance failed with "run owner unavailable" — which meant a run
	// sponsored by a platform worker (a CI agent job, the swarm scheduler) could
	// never be admitted. gRPC clients connect lazily, so this does not make the
	// two services' startup order matter.
	grpcServer.HMACSecret = cfg.HMACSecret
	if cfg.AgentsAddr == "" {
		log.Print("identity: AGENTS_ADDR is unset; delegated run grant issuance is unavailable")
	} else {
		agentsConn, err := grpc.NewClient(cfg.AgentsAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("identity: dial agent-runtime: %v", err)
		}
		defer agentsConn.Close()
		grpcServer.Agents = agentsv1.NewAgentServiceClient(agentsConn)
	}
	// An organization's deletion is announced here and every service removes
	// its own share; without the publisher DeleteOrg refuses to delete.
	grpcServer.SetOrgDeletedPublisher(identity.RedisOrgDeletedPublisher(rdb))

	srv := grpc.NewServer(grpc.UnaryInterceptor(identity.UnaryAuthInterceptor(grpcServer)))
	identityv1.RegisterIdentityServiceServer(srv, grpcServer)

	check := func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return err
		}
		return rdb.Ping(ctx).Err()
	}

	if err := service.Serve(ctx, cfg, srv, check); err != nil {
		log.Fatalf("identity: serve: %v", err)
	}
}
