// Command git-platform runs the git-platform service: repository metadata
// and history over gRPC, plus the git smart-HTTP and SSH transports.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"github.com/novaforge/novaforge/internal/svcauth"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/service"
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
	hmacSecret = cfg.HMACSecret
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

	if err := os.MkdirAll(cfg.GitDataDir, 0o755); err != nil {
		log.Fatalf("git-platform: create git data dir: %v", err)
	}

	// --- gRPC ---
	grpcServer := gitops.NewGRPCServer(pool, cfg.GitDataDir)
	srv := grpc.NewServer(grpc.UnaryInterceptor(gitopsAuthInterceptor(identityClient)))
	gitv1.RegisterGitServiceServer(srv, grpcServer)

	// --- smart-HTTP ---
	authFunc := newAuthFunc(identityClient)
	capFunc := newCapFunc(grants)
	httpHandler := gitops.NewHTTPHandler(cfg.GitDataDir, authFunc, capFunc)

	// --- SSH ---
	hostKey, ephemeral, err := loadOrGenerateHostKey()
	if err != nil {
		log.Fatalf("git-platform: ssh host key: %v", err)
	}
	if ephemeral {
		log.Printf("git-platform: SSH_HOST_KEY not set; generated an ephemeral ed25519 host key for this process")
	}
	fingerprintFunc := newFingerprintFunc(identityClient)
	sshServer := gitops.NewSSHServer(cfg.GitDataDir, hostKey, fingerprintFunc, capFunc)

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

// gitopsAuthInterceptor resolves the caller from the request's
// "authorization" metadata via the identity service (a session or personal
// access token) and attaches the resulting authz.Scope to the request
// context, so gitv1.GitServiceServer's RPCs derive their organization from
// context rather than from the request message.
func gitopsAuthInterceptor(identityClient identityv1.IdentityServiceClient) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if scope, ok := resolveScopeFromMetadata(ctx, identityClient); ok {
			ctx = authz.WithScope(ctx, scope)
		}
		return handler(ctx, req)
	}
}

// hmacSecret is the shared secret platform service tokens are signed with. It
// is set once at startup from the environment.
var hmacSecret string

func resolveScopeFromMetadata(ctx context.Context, identityClient identityv1.IdentityServiceClient) (authz.Scope, bool) {
	token := bearerTokenFromContext(ctx)
	if token == "" {
		return authz.Scope{}, false
	}
	// The organization travels in its own header. A credential says who the
	// caller is, not which organization they are acting in, and identity
	// verifies membership before granting an org scope — taking it from the
	// request message would let a caller name any organization.
	org := metadataValue(ctx, "x-novaforge-org")

	// A platform worker has no human behind it and presents a signed service
	// token naming the single organization it is acting for. It is verified
	// here rather than being let through unauthenticated, which would open the
	// same door to anyone.
	if strings.HasPrefix(token, svcauth.Prefix) {
		name, orgID, err := svcauth.Verify(hmacSecret, token)
		if err != nil {
			return authz.Scope{}, false
		}
		log.Printf("git-platform: accepted service token from %s for org %s", name, orgID)
		return authz.Scope{OrgID: orgID, ActorKind: "service", ActorID: uuid.Nil}, true
	}

	subject, err := resolveSubject(ctx, identityClient, token, org)
	if err != nil {
		return authz.Scope{}, false
	}
	return subjectToScope(subject), true
}

func resolveSubject(ctx context.Context, identityClient identityv1.IdentityServiceClient, token, org string) (*identityv1.Subject, error) {
	if resp, err := identityClient.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: token, Org: org}); err == nil {
		return resp.GetSubject(), nil
	}
	resp, err := identityClient.ResolveSession(ctx, &identityv1.ResolveSessionRequest{Token: token, Org: org})
	if err != nil {
		return nil, err
	}
	return resp.GetSubject(), nil
}

// metadataValue returns the first value of an incoming metadata key.
func metadataValue(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// subjectToScope converts an identityv1.Subject into an authz.Scope.
//
// Subject.OrgId is empty whenever the credential that resolved to this
// subject has no organization selected (personal access tokens and login
// sessions are not yet org-scoped as of this service's current RPC
// surface); the resulting Scope's OrgID is then uuid.Nil, and
// authz.RequireOrg correctly denies every organization-scoped request for
// it rather than silently granting access to none in particular.
func subjectToScope(subject *identityv1.Subject) authz.Scope {
	var scope authz.Scope
	scope.ActorID, _ = uuid.Parse(subject.GetUserId())
	scope.OrgID, _ = uuid.Parse(subject.GetOrgId())
	scope.ActorKind = subject.GetActorKind()
	scope.Role = subject.GetRole()
	if scope.ActorKind == "" {
		scope.ActorKind = "user"
	}
	return scope
}

// newAuthFunc adapts the identity service into a gitops.AuthFunc for the
// smart-HTTP transport. The HTTP Basic password carries the credential (a
// personal access token or session token); the username is conventionally
// ignored by git hosts using token auth and is not otherwise trusted.
func newAuthFunc(identityClient identityv1.IdentityServiceClient) gitops.AuthFunc {
	return func(ctx context.Context, user, pass, orgRef string) (authz.Scope, error) {
		// A CI job clones with a service token, not a person's credential. The
		// same token type is accepted on both surfaces so a job does not need a
		// second, weaker way in.
		if strings.HasPrefix(pass, svcauth.Prefix) {
			name, orgID, err := svcauth.Verify(hmacSecret, pass)
			if err != nil {
				return authz.Scope{}, fmt.Errorf("service token: %w", err)
			}
			log.Printf("git-platform: accepted service token from %s for org %s over http", name, orgID)
			return authz.Scope{OrgID: orgID, ActorKind: "service"}, nil
		}
		subject, err := resolveSubject(ctx, identityClient, pass, orgRef)
		if err != nil {
			return authz.Scope{}, fmt.Errorf("resolve credential: %w", err)
		}
		return subjectToScope(subject), nil
	}
}

// newFingerprintFunc adapts the identity service into a
// gitops.FingerprintFunc for the SSH transport.
func newFingerprintFunc(identityClient identityv1.IdentityServiceClient) gitops.FingerprintFunc {
	return func(ctx context.Context, fingerprint, orgRef string) (authz.Scope, error) {
		resp, err := identityClient.ResolveFingerprint(ctx,
			&identityv1.ResolveFingerprintRequest{Fingerprint: fingerprint, Org: orgRef})
		if err != nil {
			return authz.Scope{}, fmt.Errorf("resolve fingerprint: %w", err)
		}
		return subjectToScope(resp.GetSubject()), nil
	}
}

// newCapFunc adapts the capability store into a gitops.CapFunc: every
// requested ref update must be covered by an active grant issued to the
// caller within orgID.
// newCapFunc adapts the capability store into a gitops.CapFunc.
//
// Capability grants exist to constrain AGENTS: section 7 of the design is about
// never handing an agent a broad token. A human member of the organization has
// ordinary write access to its repositories — requiring them to mint a grant to
// push their own work would be a different product. The caller's kind, which
// identity established, decides which rule applies.
func newCapFunc(grants *capability.Store) gitops.CapFunc {
	return func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		if len(refs) == 0 {
			return nil
		}
		if s.ActorKind == "user" {
			// Org membership was verified during authentication; reaching here
			// with a user scope means the caller is a member of orgID.
			return nil
		}
		active, err := grants.ListActive(ctx, orgID, s.ActorID)
		if err != nil {
			return fmt.Errorf("resolve capability grants: %w", err)
		}
		for _, ref := range refs {
			permitted := false
			for _, g := range active {
				if capability.CanWriteRef(g, ref) == nil {
					permitted = true
					break
				}
			}
			if !permitted {
				return fmt.Errorf("write to %s not permitted by any active grant for %s %s",
					ref, s.ActorKind, s.ActorID)
			}
		}
		return nil
	}
}

// bearerTokenFromContext extracts the token from a gRPC request's
// "authorization: Bearer <token>" metadata, if present.
func bearerTokenFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return strings.TrimPrefix(values[0], "Bearer ")
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
