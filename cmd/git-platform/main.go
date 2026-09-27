// Command git-platform runs the git-platform service: repository metadata
// and history over gRPC, plus the git smart-HTTP and SSH transports.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"github.com/novaforge/novaforge/internal/egress"
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
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/cleanup"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/service"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/version"
	"github.com/novaforge/novaforge/internal/webhooks"
)

// releasesBucket is the fixed bucket release assets live in. It is separate from
// ci-runner's artifacts bucket on purpose: an artifact is evidence of one job and
// is expected to be reaped, while a release asset is a published download whose
// whole value is that it is still there in a year. Sharing one bucket would put
// both under one retention policy.
const releasesBucket = "novaforge-releases"

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
	// The webhook tables are a third such set, and must come after the
	// repositories one: a hook references a repository, which is what removes a
	// deleted repository's hooks.
	if err := database.MigrateAs(cfg.DatabaseURL, "gitplatform", "gitplatform_webhooks", webhooks.MigrationsFS); err != nil {
		log.Fatalf("git-platform: migrate gitplatform (webhooks) schema: %v", err)
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
	// A repository's deletion is announced so every service removes its share,
	// and this service removes its own share of an organization's deletion.
	// Without Redis there is nobody to announce to, and DeleteRepo refuses.
	if rdb != nil {
		grpcServer.SetRepoDeletedPublisher(gitops.RedisRepoDeletedPublisher(rdb))
		cleanup.GitPlatform(grpcServer).Run(ctx, rdb, "git-platform")
	} else {
		log.Println("git-platform: REDIS_URL is unset; repositories cannot be deleted and organization deletions are not consumed")
	}
	grpcServer.Grants = grants

	// lfsStore stays nil without object storage, and the LFS endpoints then
	// answer "not available in this deployment" rather than 404ing as if the
	// protocol were unheard of — the distinction a clone's error message needs.
	outbound, err := egress.Parse(cfg.OutboundDestinations)
	if err != nil {
		log.Fatalf("git-platform: %v", err)
	}
	var lfsStore *gitops.LFSStore

	// Releases carry uploaded files, which live in object storage next to CI
	// artifacts. A deployment without S3 configured still starts — releases
	// themselves are rows — and the release RPCs then say object storage is
	// unconfigured instead of failing obscurely. Refusing to start would take the
	// git transports down with it, which is a worse trade for an optional surface.
	if cfg.S3Endpoint == "" {
		log.Println("git-platform: S3_ENDPOINT is unset; release assets cannot be stored or served")
	} else {
		blobs, err := blobstore.New(ctx, blobstore.Options{
			Endpoint:  cfg.S3Endpoint,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Bucket:    releasesBucket,
		})
		if err != nil {
			log.Fatalf("git-platform: connect object storage: %v", err)
		}
		grpcServer.Releases = gitops.NewReleaseStore(pool, cfg.GitDataDir, blobs)
		// LFS objects share the bucket with release assets, and for the same
		// reason: both are published content whose whole value is still being
		// there in a year, unlike a CI artifact. The per-object limit is passed
		// rather than defaulted here — gitops.NewLFSStore holds the fallback, so
		// an unset variable is a bounded deployment, not an unbounded one.
		lfsStore = gitops.NewLFSStore(pool, blobs, int64(cfg.LFSMaxObjectBytes))
		go func() {
			if err := (&gitops.BlobCollector{Pool: pool, Blobs: blobs}).Run(ctx); err != nil && ctx.Err() == nil {
				log.Printf("git-platform: blob collector stopped: %v", err)
			}
		}()
	}

	// Both interceptors, not just the unary one. The release asset RPCs stream,
	// and a streaming RPC served without the stream interceptor sees no caller at
	// all: every upload and download would be refused as unauthenticated, which
	// reads as a permissions problem rather than a missing interceptor.
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(identityClient, cfg.HMACSecret)),
		grpc.StreamInterceptor(svcauth.StreamServerInterceptor(identityClient, cfg.HMACSecret)),
	)
	// --- webhooks ---
	// Hooks belong to a repository, so this service owns them and delivers them.
	// The worker is the whole feature: without it every push is consumed and
	// acknowledged and no endpoint is ever called, which is indistinguishable
	// from "nobody registered a hook" — the silence this repository has been
	// caught by more than once. internal/webhooks asserts this call exists.
	hooks := webhooks.NewStore(pool, []byte(cfg.SecretsKEK))
	grpcServer.Hooks = hooks
	if rdb != nil {
		// The consumer name is the pod's, so each replica has a stable identity
		// in the group and a crashed pod's in-flight message can be reclaimed.
		worker := &webhooks.Worker{RDB: rdb, Store: hooks, Client: outbound.Client(), Consumer: os.Getenv("HOSTNAME")}
		go func() {
			if err := worker.Run(ctx); err != nil {
				log.Printf("git-platform: webhook delivery worker stopped: %v", err)
			}
		}()
	} else {
		log.Println("git-platform: REDIS_URL is unset; webhooks can be registered but nothing will be delivered")
	}

	// --- import and mirroring ---
	// A mirror follows a repository on another Git host, which is the migration
	// path onto this platform. The mirrorer is the whole feature: without it an
	// imported repository silently never changes again, which is
	// indistinguishable from an upstream nobody has pushed to — the silence this
	// repository has been caught by more than once. internal/gitops asserts this
	// call exists.
	mirrors := gitops.NewMirrorStore(pool, cfg.GitDataDir, []byte(cfg.SecretsKEK))
	mirrors.Outbound = outbound
	grpcServer.Mirrors = mirrors
	if cfg.SecretsKEK == "" {
		log.Println("git-platform: SECRETS_KEK is unset; a repository can be imported from a public remote, but no upstream credential can be stored")
	}
	mirrorer := &gitops.Mirrorer{Store: mirrors, Tick: time.Minute}
	go func() {
		if err := mirrorer.Run(ctx); err != nil {
			log.Printf("git-platform: mirrorer stopped: %v", err)
		}
	}()

	gitv1.RegisterGitServiceServer(srv, grpcServer)

	// --- smart-HTTP ---
	capFunc := newCapFunc(grants,
		newBranchLockGuard(agentsClient, cfg.HMACSecret, repoIDResolver(pool)),
		newArchiveGuard(pool),
		newCollaboratorGuard(repoIDResolver(pool)),
		// A mirror's history belongs to upstream, so a push here is refused on
		// both transports from the one place they share.
		gitops.NewMirrorCapFunc(pool))
	// Branches written through the API answer to the same rules as a push.
	grpcServer.RefGuard = capFunc
	// Repository grants are resolved here so someone outside the organization can
	// authenticate against a repository they were granted. A grant naming a team is
	// resolved through Identity's RPC, because teams live in its schema.
	collaborators := gitops.NewCollaboratorStore(pool, gitops.NewIdentityTeamLookup(identityClient, cfg.HMACSecret))
	grpcServer.Collaborators = collaborators
	// A grant may name a person by username, which only Identity can resolve.
	grpcServer.Users = gitops.NewIdentityUserResolver(identityClient)
	// LFS is served by this same handler, with this same AuthFunc and capFunc:
	// an LFS object must be reachable exactly when the repository is, and a
	// second handler with its own authenticator is how that stops being true.
	keyLookup := gitops.NewFingerprintFunc(identityClient, collaborators)
	passwordLookup := gitops.NewAgentPasswordFunc(cfg.HMACSecret)
	var lfsAuth *gitops.LFSAuth
	if lfsStore != nil && cfg.GitPublicURL != "" {
		lfsAuth, err = gitops.NewLFSAuth(cfg.HMACSecret, cfg.GitPublicURL, lfsStore, keyLookup, passwordLookup)
		if err != nil {
			log.Fatalf("git-platform: SSH LFS: %v", err)
		}
	}
	httpHandler := gitops.NewHTTPHandlerWithLFS(cfg.GitDataDir,
		gitops.NewCredentialAuthFunc(identityClient, cfg.HMACSecret, collaborators), capFunc, lfsStore, lfsAuth)

	// --- SSH ---
	hostKey, ephemeral, err := loadOrGenerateHostKey()
	if err != nil {
		log.Fatalf("git-platform: ssh host key: %v", err)
	}
	if ephemeral {
		log.Printf("git-platform: SSH_HOST_KEY not set; generated an ephemeral ed25519 host key for this process")
	}
	sshServer := gitops.NewSSHServer(cfg.GitDataDir, hostKey, keyLookup, capFunc).
		WithPasswords(passwordLookup).WithLFS(lfsAuth)

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
	errCh := make(chan error, 3)

	// --- TLS smart-HTTP ---
	// A clone or push over the plaintext port sends its credential in the clear,
	// which is acceptable only between pods. The TLS port is the one published
	// outside the cluster, and the plaintext one stays bound so nothing already
	// dialling it in-cluster breaks. Both files or neither: one without the
	// other means an operator asked for TLS and would otherwise get a service
	// that silently serves only plaintext, which is the failure to avoid.
	var tlsSrv *http.Server
	switch {
	case cfg.GitTLSCertFile != "" && cfg.GitTLSKeyFile != "":
		tlsSrv, err = gitops.NewTLSServer(httpHandler, cfg.GitTLSCertFile, cfg.GitTLSKeyFile)
		if err != nil {
			log.Fatalf("git-platform: tls keypair: %v", err)
		}
	case cfg.GitTLSCertFile != "" || cfg.GitTLSKeyFile != "":
		log.Fatal("git-platform: NF_GIT_TLS_CERT_FILE and NF_GIT_TLS_KEY_FILE must be set together")
	default:
		log.Printf("git-platform: NF_GIT_TLS_CERT_FILE is unset; the git transport is served in plaintext only")
	}

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

	var tlsListener net.Listener
	if tlsSrv != nil {
		tlsListener, err = net.Listen("tcp", fmt.Sprintf(":%d", gitops.DefaultHTTPSPort))
		if err != nil {
			log.Fatalf("git-platform: listen https: %v", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Printf("git-platform: smart-http listening on :%d over tls", gitops.DefaultHTTPSPort)
			// The keypair is already bound to the server, so ServeTLS is given
			// no paths; passing them again would reload them once and lose the
			// per-handshake reload a renewed certificate needs.
			if err := tlsSrv.ServeTLS(tlsListener, "", ""); err != nil {
				errCh <- fmt.Errorf("smart-http tls server: %w", err)
			}
		}()
	}

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
	if tlsListener != nil {
		_ = tlsListener.Close()
	}
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
func newCapFunc(grants *capability.Store, locks, archived, collaborator, mirrored gitops.CapFunc) gitops.CapFunc {
	byGrant := gitops.NewGrantCapFunc(grants)
	return func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		// An outside collaborator is checked for reads as well as writes, so this
		// runs before the early return for an empty ref set: CapFunc is called with
		// no refs for git-upload-pack, which is exactly the read that has to be
		// confined to the repositories they hold.
		if err := collaborator(ctx, s, orgID, repo, refs); err != nil {
			return err
		}
		if len(refs) == 0 {
			return nil
		}
		// An archived repository refuses writes before anything else is
		// considered: whether the pusher holds a grant is not the question. A
		// mirror is the same kind of refusal — its history is upstream's — so it
		// is answered here too, before any grant is looked at.
		if err := archived(ctx, s, orgID, repo, refs); err != nil {
			return err
		}
		if err := mirrored(ctx, s, orgID, repo, refs); err != nil {
			return err
		}
		if err := locks(ctx, s, orgID, repo, refs); err != nil {
			return err
		}
		return byGrant(ctx, s, orgID, repo, refs)
	}
}
