// Package platformtest starts the NovaForge platform in one process for tests
// that must cross service boundaries: every service's real gRPC server behind
// the real credential interceptor, reached over real loopback gRPC, on the
// real dev PostgreSQL and Redis (and MinIO, for CI), with the git transports
// using git-platform's real credential adapter and the REST edge in front.
//
// It exists because the defects this platform keeps finding are seams: two
// components each correct and each tested against a double of the other, and
// nothing connecting them. A test that starts here starts from a credential
// identity issued, and every hop after that is the hop production makes. The
// only thing absent is Kubernetes: agent runs are started and recorded but
// never executed, since executing them needs a cluster and a model.
//
// It is imported only by tests. Without TEST_DATABASE_URL and TEST_REDIS_URL
// Start skips the calling test, which reads as a pass — source hack/env.sh.
package platformtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/identity"
	"github.com/novaforge/novaforge/internal/mcp"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/secrets"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/work"
)

// HMACSecret signs the platform credentials minted inside this process.
const HMACSecret = "platformtest-hmac-secret"

// Platform is one running in-process platform.
type Platform struct {
	Pool   *pgxpool.Pool
	Redis  *redis.Client
	Grants *capability.Store
	// GitRoot is where git-platform keeps bare repositories.
	GitRoot string

	// Addresses of each service's gRPC listener.
	IdentityAddr, GitAddr, WorkAddr, GatesAddr, AgentsAddr, MCPAddr, CIAddr string

	// GitHTTPURL is the smart-HTTP transport's base URL; SSHAddr the SSH
	// transport's host:port; EdgeURL the REST edge's base URL; MCPURL the
	// Streamable HTTP endpoint of NovaForge's own MCP server.
	GitHTTPURL, SSHAddr, EdgeURL, MCPURL string

	// MCPServer is the MCP server behind MCPURL, over the production backend,
	// for tests that drive the stdio transport of the same server.
	MCPServer *mcp.Server

	// Clients carrying no credential of their own: a test attaches one with
	// AsUser, exactly as a caller would.
	Identity identityv1.IdentityServiceClient
	Git      gitv1.GitServiceClient
	Work     workv1.WorkServiceClient
	Reviews  reviewsv1.ReviewsServiceClient
	Gates    gatesv1.GatesServiceClient
	Agents   agentsv1.AgentServiceClient
	MCP      mcpv1.McpServiceClient
	// CI is nil unless TEST_S3_ENDPOINT is set; RequireCI skips without it.
	CI civ1.CIServiceClient

	AgentStore *agents.Store
	CIStore    *ci.Store
}

// Start brings the platform up for t and tears it down when t ends.
func Start(t testing.TB) *Platform {
	t.Helper()
	dbURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required (source hack/env.sh)")
	}
	migrateAll(t, dbURL)

	ctx := context.Background()
	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("platformtest: connect database: %v", err)
	}
	t.Cleanup(pool.Close)
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("platformtest: parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })

	p := &Platform{Pool: pool, Redis: rdb, Grants: capability.NewStore(pool), GitRoot: t.TempDir()}

	// identity
	idSrv := identity.NewGRPCServer(identity.NewStore(pool), identity.NewSessionStore(rdb),
		identity.NewTokenStore(pool), identity.NewSSHKeyStore(pool), p.Grants)
	p.IdentityAddr = serve(t, func(s *grpc.Server) { identityv1.RegisterIdentityServiceServer(s, idSrv) },
		grpc.UnaryInterceptor(identity.UnaryAuthInterceptor(idSrv)))
	p.Identity = identityv1.NewIdentityServiceClient(dial(t, p.IdentityAddr, nil))
	// A service calling identity on a caller's behalf forwards the caller's
	// credential, as every service's identity connection that needs it does.
	identityFwd := identityv1.NewIdentityServiceClient(dial(t, p.IdentityAddr, svcauth.ForwardIncomingCredential))

	interceptor := grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(p.Identity, HMACSecret))

	// git-platform: gRPC, smart-HTTP and SSH, composed as cmd/git-platform does.
	gitSrv := gitops.NewGRPCServer(pool, p.GitRoot)
	gitSrv.Grants = p.Grants
	p.GitAddr = serve(t, func(s *grpc.Server) { gitv1.RegisterGitServiceServer(s, gitSrv) }, interceptor)
	capFunc := gitops.NewGrantCapFunc(p.Grants)
	httpSrv := httptest.NewServer(gitops.NewHTTPHandler(p.GitRoot, gitops.NewCredentialAuthFunc(p.Identity, HMACSecret), capFunc))
	t.Cleanup(httpSrv.Close)
	p.GitHTTPURL = httpSrv.URL
	sshSrv := gitops.NewSSHServer(p.GitRoot, hostKey(t), gitops.NewFingerprintFunc(p.Identity), capFunc).
		WithPasswords(gitops.NewAgentPasswordFunc(HMACSecret))
	sshLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("platformtest: listen ssh: %v", err)
	}
	go func() { _ = sshSrv.Serve(sshLis) }()
	t.Cleanup(func() { _ = sshLis.Close() })
	p.SSHAddr = sshLis.Addr().String()

	gitFwd := gitv1.NewGitServiceClient(dial(t, p.GitAddr, svcauth.ForwardIncomingCredential))
	p.Git = gitv1.NewGitServiceClient(dial(t, p.GitAddr, nil))

	// The addresses of work-reviews and gates are needed by each other, so
	// both listeners exist before either server is built.
	workLis, gatesLis := listen(t), listen(t)
	p.WorkAddr, p.GatesAddr = workLis.Addr().String(), gatesLis.Addr().String()
	workConnFwd := dial(t, p.WorkAddr, svcauth.ForwardIncomingCredential)
	gatesConnFwd := dial(t, p.GatesAddr, svcauth.ForwardIncomingCredential)

	// work-reviews, with the merger joined to the real gate service.
	workStore, reviewsStore := work.NewStore(pool), reviews.NewStore(pool)
	reviewsSrv := reviews.NewGRPCServer(reviewsStore)
	reviewsSrv.Merger = &reviews.Merger{
		Store: reviewsStore,
		Gates: reviews.GatesClient{Gates: gatesv1.NewGatesServiceClient(gatesConnFwd)},
		Git:   gitFwd,
	}
	serveOn(t, workLis, func(s *grpc.Server) {
		workv1.RegisterWorkServiceServer(s, work.NewGRPCServer(workStore))
		reviewsv1.RegisterReviewsServiceServer(s, reviewsSrv)
	}, interceptor)

	// gates, with the controller the gates service composes.
	controller := gates.NewController(gates.NewStore(pool), gitFwd,
		reviewsv1.NewReviewsServiceClient(workConnFwd), workv1.NewWorkServiceClient(workConnFwd), "")
	gatesSrv := gates.NewGRPCServer(controller, approvals.NewStore(pool), secrets.NewBroker(pool, []byte("platformtest-kek")), p.Grants)
	gatesSrv.Proposals = &gates.Proposer{Git: gitFwd, Reviews: reviewsv1.NewReviewsServiceClient(workConnFwd)}
	serveOn(t, gatesLis, func(s *grpc.Server) { gatesv1.RegisterGatesServiceServer(s, gatesSrv) }, interceptor)

	p.Work = workv1.NewWorkServiceClient(dial(t, p.WorkAddr, nil))
	p.Reviews = reviewsv1.NewReviewsServiceClient(dial(t, p.WorkAddr, nil))
	p.Gates = gatesv1.NewGatesServiceClient(dial(t, p.GatesAddr, nil))

	// agent-runtime, with no executor: a run is started, granted and
	// recorded, and never executed, because execution needs Kubernetes.
	p.AgentStore = agents.NewStore(pool)
	agentsSrv := agents.NewGRPCServer(p.AgentStore, p.Grants, rdb, workv1.NewWorkServiceClient(workConnFwd), nil)
	agentsSrv.Audit = agents.NewAuditLog(pool)
	p.AgentsAddr = serve(t, func(s *grpc.Server) { agentsv1.RegisterAgentServiceServer(s, agentsSrv) }, interceptor)
	p.Agents = agentsv1.NewAgentServiceClient(dial(t, p.AgentsAddr, nil))

	// mcp-server: the register over gRPC, and NovaForge's own MCP server over
	// the production backend on Streamable HTTP.
	p.MCPAddr = serve(t, func(s *grpc.Server) { mcpv1.RegisterMcpServiceServer(s, mcp.NewRegistry(pool, identityFwd)) }, interceptor)
	p.MCP = mcpv1.NewMcpServiceClient(dial(t, p.MCPAddr, nil))

	// ci-runner, when object storage is available.
	var ciEdge civ1.CIServiceClient
	if ep := os.Getenv("TEST_S3_ENDPOINT"); ep != "" {
		blobs, err := blobstore.New(ctx, blobstore.Options{
			Endpoint: ep, AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
			Bucket: "novaforge-test-platform",
		})
		if err != nil {
			t.Fatalf("platformtest: object storage: %v", err)
		}
		svc := ci.NewService(pool, rdb, blobs, gitFwd, HMACSecret, p.GitHTTPURL)
		p.CIStore = svc.Store
		p.CIAddr = serve(t, func(s *grpc.Server) {
			civ1.RegisterRunnerServiceServer(s, svc.Server)
			civ1.RegisterCIServiceServer(s, svc.Query)
		}, interceptor)
		p.CI = civ1.NewCIServiceClient(dial(t, p.CIAddr, nil))
		ciEdge = civ1.NewCIServiceClient(dial(t, p.CIAddr, edge.ForwardCredential))
	}

	p.MCPServer = mcp.NewServer(mcp.NewPlatformBackend(mcp.PlatformClients{
		Identity: p.Identity, Git: p.Git, Work: p.Work, Reviews: p.Reviews, Gates: p.Gates, CI: p.CI,
	}))
	mcpHTTP := httptest.NewServer(p.MCPServer)
	t.Cleanup(mcpHTTP.Close)
	p.MCPURL = mcpHTTP.URL

	// The REST edge, composed as cmd/edge composes it.
	edgeDial := func(addr string) *grpc.ClientConn { return dial(t, addr, edge.ForwardCredential) }
	workEdge := edgeDial(p.WorkAddr)
	ecfg := edge.Config{
		Identity: identityv1.NewIdentityServiceClient(edgeDial(p.IdentityAddr)),
		Git:      gitv1.NewGitServiceClient(edgeDial(p.GitAddr)),
		Work:     workv1.NewWorkServiceClient(workEdge),
		Reviews:  reviewsv1.NewReviewsServiceClient(workEdge),
		Agents:   agentsv1.NewAgentServiceClient(edgeDial(p.AgentsAddr)),
		Gates:    gatesv1.NewGatesServiceClient(edgeDial(p.GatesAddr)),
		MCP:      mcpv1.NewMcpServiceClient(edgeDial(p.MCPAddr)),
		CI:       ciEdge,
	}
	ecfg.Handlers = edge.Handlers(ecfg)
	edgeSrv := httptest.NewServer(edge.NewRouter(ecfg))
	t.Cleanup(edgeSrv.Close)
	p.EdgeURL = edgeSrv.URL
	return p
}

// RequireCI skips t when this platform has no CI service.
func (p *Platform) RequireCI(t testing.TB) {
	t.Helper()
	if p.CI == nil {
		t.Skip("TEST_S3_ENDPOINT is required for the CI service (source hack/env.sh)")
	}
}

// migrateAll applies every schema the platform's services own, as each
// service's main does.
func migrateAll(t testing.TB, url string) {
	t.Helper()
	type migration struct {
		schema string
		apply  func() error
	}
	for _, step := range []migration{
		{"identity", func() error { return database.Migrate(url, "identity", identity.MigrationsFS) }},
		{"gitplatform", func() error { return database.Migrate(url, "gitplatform", capability.MigrationsFS) }},
		{"gitplatform", func() error {
			return database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS)
		}},
		{"work", func() error { return database.Migrate(url, "work", work.MigrationsFS) }},
		{"reviews", func() error { return database.Migrate(url, "reviews", reviews.MigrationsFS) }},
		{"gates", func() error { return database.Migrate(url, "gates", gates.MigrationsFS) }},
		{"approvals", func() error { return database.Migrate(url, "approvals", approvals.MigrationsFS) }},
		{"secrets", func() error { return database.Migrate(url, "secrets", secrets.MigrationsFS) }},
		{"agents", func() error { return database.Migrate(url, "agents", agents.MigrationsFS) }},
		{"ci", func() error { return database.Migrate(url, "ci", ci.MigrationsFS) }},
		{"mcp", func() error { return database.Migrate(url, "mcp", mcp.MigrationsFS) }},
	} {
		if err := step.apply(); err != nil {
			// The dev database is shared by every branch under development. A
			// branch that has added a migration this checkout does not have
			// leaves the schema one version ahead, which golang-migrate reports
			// as a missing file; additive migrations make that schema a
			// superset of this checkout's, so the test proceeds on it.
			if strings.Contains(err.Error(), "no migration found for version") {
				t.Logf("platformtest: %s schema is ahead of this checkout (%v); proceeding", step.schema, err)
				continue
			}
			t.Fatalf("platformtest: migrate %s: %v", step.schema, err)
		}
	}
}

func listen(t testing.TB) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("platformtest: listen: %v", err)
	}
	return lis
}

func serve(t testing.TB, register func(*grpc.Server), opts ...grpc.ServerOption) string {
	t.Helper()
	lis := listen(t)
	serveOn(t, lis, register, opts...)
	return lis.Addr().String()
}

func serveOn(t testing.TB, lis net.Listener, register func(*grpc.Server), opts ...grpc.ServerOption) {
	t.Helper()
	s := grpc.NewServer(opts...)
	register(s)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
}

func dial(t testing.TB, addr string, interceptor grpc.UnaryClientInterceptor) *grpc.ClientConn {
	t.Helper()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if interceptor != nil {
		opts = append(opts, grpc.WithChainUnaryInterceptor(interceptor))
	}
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		t.Fatalf("platformtest: dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func hostKey(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("platformtest: host key: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("platformtest: host key signer: %v", err)
	}
	return s
}

// User is a registered, logged-in person.
type User struct {
	ID       string
	Username string
	Password string
	// Session is the session token Login returned.
	Session string
}

// NewUser registers and logs in a person with a unique name.
func (p *Platform) NewUser(t testing.TB, prefix string) User {
	t.Helper()
	ctx := context.Background()
	name := prefix + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	u := User{Username: name, Password: "correct horse battery staple"}
	reg, err := p.Identity.Register(ctx, &identityv1.RegisterRequest{Email: name + "@example.com", Username: name, Password: u.Password})
	if err != nil {
		t.Fatalf("platformtest: register %s: %v", name, err)
	}
	u.ID = reg.GetUserId()
	login, err := p.Identity.Login(ctx, &identityv1.LoginRequest{Username: name, Password: u.Password})
	if err != nil {
		t.Fatalf("platformtest: login %s: %v", name, err)
	}
	u.Session = login.GetSessionToken()
	return u
}

// Org is an organization a test created.
type Org struct {
	ID, Name string
}

// NewOrg creates an organization owned by owner.
func (p *Platform) NewOrg(t testing.TB, owner User, prefix string) Org {
	t.Helper()
	name := prefix + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	resp, err := p.Identity.CreateOrg(WithCredential(context.Background(), owner.Session, ""), &identityv1.CreateOrgRequest{Name: name})
	if err != nil {
		t.Fatalf("platformtest: create org %s: %v", name, err)
	}
	return Org{ID: resp.GetOrg().GetId(), Name: name}
}

// AddMember adds member to org as role, as owner.
func (p *Platform) AddMember(t testing.TB, owner User, org Org, member User, role string) {
	t.Helper()
	if _, err := p.Identity.AddOrgMember(WithCredential(context.Background(), owner.Session, ""),
		&identityv1.AddOrgMemberRequest{OrgId: org.ID, UserId: member.ID, Role: role}); err != nil {
		t.Fatalf("platformtest: add %s to %s: %v", member.Username, org.Name, err)
	}
}

// NewPAT mints a personal access token for u and returns its id and plaintext.
func (p *Platform) NewPAT(t testing.TB, u User) (id, plaintext string) {
	t.Helper()
	resp, err := p.Identity.CreateToken(WithCredential(context.Background(), u.Session, ""),
		&identityv1.CreateTokenRequest{Name: "pat-" + uuid.NewString()[:8], Scopes: []string{"repo"}})
	if err != nil {
		t.Fatalf("platformtest: create token for %s: %v", u.Username, err)
	}
	return resp.GetToken().GetId(), resp.GetPlaintext()
}

// RevokePAT revokes u's token id.
func (p *Platform) RevokePAT(t testing.TB, u User, id string) {
	t.Helper()
	if _, err := p.Identity.DeleteToken(WithCredential(context.Background(), u.Session, ""),
		&identityv1.DeleteTokenRequest{Id: id}); err != nil {
		t.Fatalf("platformtest: revoke token %s: %v", id, err)
	}
}

// AddSSHKey registers a fresh ed25519 key for u and returns the path of its
// private key file, for ssh -i.
func (p *Platform) AddSSHKey(t testing.TB, u User) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("platformtest: generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("platformtest: signer: %v", err)
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if _, err := p.Identity.AddSSHKey(WithCredential(context.Background(), u.Session, ""),
		&identityv1.AddSSHKeyRequest{Title: "platformtest", Key: authorized}); err != nil {
		t.Fatalf("platformtest: add ssh key for %s: %v", u.Username, err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("platformtest: marshal key: %v", err)
	}
	path := t.TempDir() + "/id_ed25519"
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("platformtest: write key: %v", err)
	}
	return path
}

// WithCredential returns ctx carrying credential (and org, when non-empty) as
// outbound gRPC metadata — what every client of a NovaForge service sends.
func WithCredential(ctx context.Context, credential, org string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+credential)
	if org != "" {
		md.Append("x-novaforge-org", org)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

// AsUser returns a context acting as u within org.
func (p *Platform) AsUser(u User, org Org) context.Context {
	return WithCredential(context.Background(), u.Session, org.Name)
}

// AgentCredential mints the credential an agent run presents for agentID in
// org: exactly what agent-runtime mints.
func (p *Platform) AgentCredential(t testing.TB, org Org, agentID string) string {
	t.Helper()
	tok, err := svcauth.MintAgentRun(HMACSecret, uuid.MustParse(org.ID), uuid.MustParse(agentID), 10*time.Minute)
	if err != nil {
		t.Fatalf("platformtest: mint agent credential: %v", err)
	}
	return tok
}

// CloneURL is the smart-HTTP URL of org/repo carrying credential.
func (p *Platform) CloneURL(org Org, repo, credential string) string {
	host := strings.TrimPrefix(p.GitHTTPURL, "http://")
	return fmt.Sprintf("http://git:%s@%s/%s/%s.git", credential, host, org.Name, repo)
}
