// Command git-platform runs the git-platform service: repository metadata
// and history over gRPC, plus the git smart-HTTP and SSH transports.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/service"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/version"
)

const (
	defaultGRPCPort = 9092
	defaultHTTPPort = 8081
	defaultSSHPort  = 2222
)

func main() {
	if err := version.Require("git", "2.40.0"); err != nil {
		log.Fatalf("git-platform: %v", err)
	}

	cfg := service.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("git-platform: DATABASE_URL is required")
	}
	if cfg.IdentityAddr == "" {
		log.Fatal("git-platform: IDENTITY_ADDR is required")
	}
	if cfg.GRPCPort == 0 {
		cfg.GRPCPort = defaultGRPCPort
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = defaultHTTPPort
	}
	if cfg.SSHPort == 0 {
		cfg.SSHPort = defaultSSHPort
	}

	// capability_grants is folded in here (using the default, schema-named
	// tracking table, the same one the identity service uses) so
	// git-platform's CapFunc has somewhere to read grants from even on a
	// fresh database the identity service hasn't touched yet.
	if err := database.Migrate(cfg.DatabaseURL, "gitplatform", capability.MigrationsFS); err != nil {
		log.Fatalf("git-platform: migrate gitplatform (capability) schema: %v", err)
	}
	// repositories gets its own tracking table (see MigrationsFS's doc
	// comment) since it is a second, independently versioned migration set
	// applied to the same "gitplatform" schema.
	if err := database.MigrateAs(cfg.DatabaseURL, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		log.Fatalf("git-platform: migrate gitplatform (repositories) schema: %v", err)
	}

	ctx := context.Background()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("git-platform: connect database: %v", err)
	}
	defer pool.Close()

	var rdb *redis.Client
	if cfg.RedisURL != "" {
		redisOpts, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			log.Fatalf("git-platform: parse redis url: %v", err)
		}
		rdb = redis.NewClient(redisOpts)
		defer rdb.Close()
		// Resolve the repository id for each event: a push event with a nil id
		// is useless to the CI scheduler, which looks the repository up by it.
		gitops.WireRedisPushEventsWithRepoID(rdb, func(ctx context.Context, orgID uuid.UUID, repo string) (uuid.UUID, error) {
			var id uuid.UUID
			err := pool.QueryRow(ctx,
				`SELECT id FROM gitplatform.repositories WHERE org_id = $1 AND name = $2`,
				orgID, repo).Scan(&id)
			return id, err
		})
	}

	identityConn, err := grpc.NewClient(cfg.IdentityAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("git-platform: dial identity service: %v", err)
	}
	defer identityConn.Close()
	identityClient := identityv1.NewIdentityServiceClient(identityConn)

	grants := capability.NewStore(pool)

	// Agent-runtime answers whether an Agent Run holds a branch. It is
	// required rather than optional: a git-platform started without it would
	// refuse every push to an agent branch (the lock check fails closed), and
	// one that skipped the check would let people push into running agents'
	// work — neither should look like a configured deployment.
	if cfg.AgentsAddr == "" {
		log.Fatal("git-platform: AGENTS_ADDR is required to enforce agent branch locks")
	}
	agentsConn, err := grpc.NewClient(cfg.AgentsAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("git-platform: dial agent-runtime: %v", err)
	}
	defer agentsConn.Close()
	agentsClient := agentsv1.NewAgentServiceClient(agentsConn)

	if err := os.MkdirAll(cfg.GitDataDir, 0o755); err != nil {
		log.Fatalf("git-platform: create git data dir: %v", err)
	}

	// Every surface — gRPC, smart-HTTP, SSH — resolves a credential through
	// internal/gitops's adapter and the shared svcauth classification, so an
	// agent run's credential is the same agent, held to the same grant, on
	// all three. This service used to carry its own copy that called an agent
	// run a "service": the transports refused its pushes inside its grant and
	// the API let it write anywhere.

	// --- gRPC ---
	grpcServer := gitops.NewGRPCServer(pool, cfg.GitDataDir)
	grpcServer.Grants = grants
	srv := grpc.NewServer(grpc.UnaryInterceptor(
		svcauth.UnaryServerInterceptor(identityClient, cfg.HMACSecret)))
	gitv1.RegisterGitServiceServer(srv, grpcServer)

	// --- smart-HTTP ---
	capFunc := newCapFunc(grants, newBranchLockGuard(agentsClient, cfg.HMACSecret, repoIDResolver(pool)))
	// Branches written through the API answer to the same rules as a push.
	grpcServer.RefGuard = capFunc
	httpHandler := gitops.NewHTTPHandler(cfg.GitDataDir,
		gitops.NewCredentialAuthFunc(identityClient, cfg.HMACSecret), capFunc)

	// --- SSH ---
	hostKey, ephemeral, err := loadOrGenerateHostKey()
	if err != nil {
		log.Fatalf("git-platform: ssh host key: %v", err)
	}
	if ephemeral {
		log.Printf("git-platform: SSH_HOST_KEY not set; generated an ephemeral ed25519 host key for this process")
	}
	sshServer := gitops.NewSSHServer(cfg.GitDataDir, hostKey, gitops.NewFingerprintFunc(identityClient), capFunc).
		WithPasswords(gitops.NewAgentPasswordFunc(cfg.HMACSecret))

	check := func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		if rdb != nil {
			if err := rdb.Ping(ctx).Err(); err != nil {
				return fmt.Errorf("redis: %w", err)
			}
		}
		return nil
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	httpListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.HTTPPort))
	if err != nil {
		log.Fatalf("git-platform: listen http: %v", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("git-platform: smart-http listening on :%d", cfg.HTTPPort)
		httpSrv := &http.Server{Handler: httpHandler, ReadHeaderTimeout: 10 * time.Second}
		if err := httpSrv.Serve(httpListener); err != nil {
			errCh <- fmt.Errorf("smart-http server: %w", err)
		}
	}()

	sshListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.SSHPort))
	if err != nil {
		log.Fatalf("git-platform: listen ssh: %v", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("git-platform: ssh listening on :%d", cfg.SSHPort)
		if err := sshServer.Serve(sshListener); err != nil {
			errCh <- fmt.Errorf("ssh server: %w", err)
		}
	}()

	go func() {
		if err := <-errCh; err != nil {
			log.Printf("git-platform: %v", err)
		}
	}()

	if err := service.Serve(ctx, cfg, srv, check); err != nil {
		log.Fatalf("git-platform: serve: %v", err)
	}

	_ = httpListener.Close()
	_ = sshListener.Close()
	wg.Wait()
}

// loadOrGenerateHostKey reads an ed25519 PEM private key from SSH_HOST_KEY,
// or generates one and reports that it is ephemeral.
func loadOrGenerateHostKey() (ssh.Signer, bool, error) {
	if pemData := os.Getenv("SSH_HOST_KEY"); pemData != "" {
		signer, err := ssh.ParsePrivateKey([]byte(pemData))
		if err != nil {
			return nil, false, fmt.Errorf("parse SSH_HOST_KEY: %w", err)
		}
		return signer, false, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate ed25519 host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, false, fmt.Errorf("build host key signer: %w", err)
	}
	return signer, true, nil
}

// newCapFunc is the capability check every write surface applies: a branch an
// Agent Run holds is refused to everyone but that run's agent, and an agent is
// held to its capability grant.
func newCapFunc(grants *capability.Store, locks gitops.CapFunc) gitops.CapFunc {
	byGrant := gitops.NewGrantCapFunc(grants)
	return func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		if len(refs) == 0 {
			return nil
		}
		if err := locks(ctx, s, orgID, repo, refs); err != nil {
			return err
		}
		return byGrant(ctx, s, orgID, repo, refs)
	}
}
