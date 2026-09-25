package gates_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/identity"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// platformStack is the merge path as it is deployed: identity, git-platform,
// work-reviews' reviews service and gates, each a real gRPC server on a real
// socket behind the real svcauth interceptor, over real PostgreSQL, Redis and
// git. A person's credential is resolved by identity exactly as in production
// and forwarded service to service by the same client interceptor.
//
// It exists because every merge defect found so far lived between services
// that were each unit-tested: work-reviews called gates with no credential,
// gates evaluated the target branch instead of the change, and nothing ever
// asked for an approval. A stack wired from the production constructors is
// the only place those can show.
type platformStack struct {
	t        *testing.T
	hmac     string
	root     string
	identity *identity.Server
	gitSrv   *gitops.Server

	git     gitv1.GitServiceClient
	reviews reviewsv1.ReviewsServiceClient
	gates   gatesv1.GatesServiceClient

	org    uuid.UUID
	repo   string
	repoID string
	bare   string

	owner, admin, member, author stackUser
}

type stackUser struct {
	id      uuid.UUID
	name    string
	session string
}

func stackRedis(t *testing.T) *redis.Client {
	t.Helper()
	u := os.Getenv("TEST_REDIS_URL")
	if u == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(u)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { c.Close() })
	return c
}

// serve starts srv on a loopback socket and returns a client connection to
// it that forwards the caller's incoming credential, as every production
// client between services does.
func serve(t *testing.T, register func(*grpc.Server), interceptor grpc.UnaryServerInterceptor) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	opts := []grpc.ServerOption{}
	if interceptor != nil {
		opts = append(opts, grpc.UnaryInterceptor(interceptor))
	}
	srv := grpc.NewServer(opts...)
	register(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// stackSandbox builds the isolated analysis sandbox the gates in this stack run
// repository code inside.
//
// There is no in-process alternative on purpose: running a repository's tests
// and compiler in the gates service's own process would give code under review
// the service's database handles and cluster credentials, so the controller
// refuses to evaluate an executable gate without a sandbox. That makes these
// tests need a real cluster and a real image, which hack/env.sh supplies from
// the digest hack/build-images.sh recorded.
func stackSandbox(t *testing.T) *gates.AnalysisSandbox {
	t.Helper()
	image := os.Getenv("NF_GATE_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("NF_GATE_SANDBOX_TEST_IMAGE is unset: executable gates cannot run (./hack/build-images.sh gate-analysis)")
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: "kw"}).ClientConfig()
	if err != nil {
		t.Fatalf("kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("kube client: %v", err)
	}
	sandbox, err := gates.NewAnalysisSandbox(client, config, image)
	if err != nil {
		t.Fatalf("analysis sandbox: %v", err)
	}
	return sandbox
}

func newPlatformStack(t *testing.T) *platformStack {
	t.Helper()
	url := dbURL(t)
	rdb := stackRedis(t)
	for _, m := range []struct {
		schema string
		fs     func() error
	}{
		{"identity", func() error { return database.Migrate(url, "identity", identity.MigrationsFS) }},
		{"gitplatform", func() error { return database.Migrate(url, "gitplatform", capability.MigrationsFS) }},
		{"gitplatform_git", func() error {
			return database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS)
		}},
		{"reviews", func() error { return database.Migrate(url, "reviews", reviews.MigrationsFS) }},
		{"gates", func() error { return database.Migrate(url, "gates", gates.MigrationsFS) }},
		{"approvals", func() error { return database.Migrate(url, "approvals", approvals.MigrationsFS) }},
	} {
		if err := m.fs(); err != nil {
			t.Fatalf("migrate %s: %v", m.schema, err)
		}
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	s := &platformStack{t: t, hmac: "stack-hmac-" + uuid.NewString(), root: t.TempDir()}

	s.identity = identity.NewGRPCServer(identity.NewStore(pool), identity.NewSessionStore(rdb),
		identity.NewTokenStore(pool), identity.NewSSHKeyStore(pool), capability.NewStore(pool))
	identityConn := serve(t, func(g *grpc.Server) { identityv1.RegisterIdentityServiceServer(g, s.identity) }, nil)
	auth := svcauth.UnaryServerInterceptor(identityv1.NewIdentityServiceClient(identityConn), s.hmac)

	s.gitSrv = gitops.NewGRPCServer(pool, s.root)
	gitConn := serve(t, func(g *grpc.Server) { gitv1.RegisterGitServiceServer(g, s.gitSrv) }, auth)
	s.git = gitv1.NewGitServiceClient(gitConn)

	// reviews and gates call each other, so both listeners exist before
	// either server is built.
	reviewsLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	reviewsConn, err := grpc.NewClient(reviewsLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		t.Fatalf("dial reviews: %v", err)
	}
	t.Cleanup(func() { reviewsConn.Close() })
	s.reviews = reviewsv1.NewReviewsServiceClient(reviewsConn)

	controller := gates.NewController(gates.NewStore(pool), s.git, s.reviews, nil, "",
		gates.WithProofService(s.hmac), gates.WithAnalysisSandbox(stackSandbox(t)))
	gatesSrv := gates.NewGRPCServer(controller, approvals.NewStore(pool), nil, capability.NewStore(pool))
	gatesSrv.Proposals = &gates.Proposer{Git: s.git, Reviews: s.reviews}
	gatesConn := serve(t, func(g *grpc.Server) { gatesv1.RegisterGatesServiceServer(g, gatesSrv) }, auth)
	s.gates = gatesv1.NewGatesServiceClient(gatesConn)

	reviewsSrv := reviews.NewGRPCServer(reviews.NewStore(pool))
	// Review admission must resolve the reviewed source through its real owner,
	// just as work-reviews does in production; a Merger-only Git client is not
	// available to the independent SubmitReview path.
	reviewsSrv.Git = s.git
	reviewsSrv.Merger = &reviews.Merger{
		Store: reviewsSrv.Store,
		Gates: reviews.GatesClient{Gates: s.gates},
		Git:   s.git,
	}
	rs := grpc.NewServer(grpc.UnaryInterceptor(auth))
	reviewsv1.RegisterReviewsServiceServer(rs, reviewsSrv)
	go func() { _ = rs.Serve(reviewsLis) }()
	t.Cleanup(rs.Stop)

	s.owner = s.register("owner")
	s.admin = s.register("admin")
	s.member = s.register("member")
	s.author = s.register("author")

	ownerScope := authz.WithScope(context.Background(), authz.Scope{ActorID: s.owner.id, ActorKind: "user"})
	org, err := s.identity.CreateOrg(ownerScope, &identityv1.CreateOrgRequest{Name: "stack-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	s.org = uuid.MustParse(org.GetOrg().GetId())
	for _, m := range []struct {
		u    stackUser
		role string
	}{{s.admin, "admin"}, {s.member, "member"}, {s.author, "member"}} {
		if _, err := s.identity.AddOrgMember(ownerScope, &identityv1.AddOrgMemberRequest{
			OrgId: s.org.String(), UserId: m.u.id.String(), Role: m.role,
		}); err != nil {
			t.Fatalf("AddOrgMember %s: %v", m.role, err)
		}
	}

	s.repo = "stack-" + uuid.NewString()[:8]
	created, err := s.git.CreateRepo(s.as(s.owner), &gitv1.CreateRepoRequest{Name: s.repo})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	s.repoID = created.GetRepo().GetId()
	s.bare = filepath.Join(s.root, s.org.String(), s.repo+".git")
	return s
}

func (s *platformStack) register(prefix string) stackUser {
	s.t.Helper()
	name := prefix + "-" + uuid.NewString()[:8]
	password := "pw-" + uuid.NewString()
	reg, err := s.identity.Register(context.Background(), &identityv1.RegisterRequest{
		Email: name + "@example.com", Username: name, Password: password,
	})
	if err != nil {
		s.t.Fatalf("Register %s: %v", name, err)
	}
	login, err := s.identity.Login(context.Background(), &identityv1.LoginRequest{Username: name, Password: password})
	if err != nil {
		s.t.Fatalf("Login %s: %v", name, err)
	}
	return stackUser{id: uuid.MustParse(reg.GetUserId()), name: name, session: login.GetSessionToken()}
}

// as returns a context carrying u's session credential for the stack's
// organization, the way the edge presents a person to the services.
func (s *platformStack) as(u stackUser) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+u.session, "x-novaforge-org", s.org.String())
}

// asAgent returns a context carrying an agent run's service token, which is
// how an agent reaches the services.
func (s *platformStack) asAgent() context.Context {
	tok, err := svcauth.Mint(s.hmac, svcauth.AgentRunService, s.org, time.Hour)
	if err != nil {
		s.t.Fatalf("mint agent token: %v", err)
	}
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+tok, "x-novaforge-org", s.org.String())
}

// commit writes files (a nil value deletes the path) on branch, starting it
// from `from` when it does not exist yet, pushes it straight into the bare
// repository and returns the new head.
func (s *platformStack) commit(branch, from string, files map[string]*string) string {
	s.t.Helper()
	work := s.t.TempDir()
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			s.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "clone", "-q", s.bare, work)
	if from == "" {
		run(work, "checkout", "-q", "-b", branch)
	} else if out, _ := exec.Command("git", "--git-dir="+s.bare, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Output(); len(out) > 0 {
		run(work, "checkout", "-q", branch)
	} else {
		run(work, "checkout", "-q", "-b", branch, "origin/"+from)
	}
	for path, body := range files {
		full := filepath.Join(work, path)
		if body == nil {
			run(work, "rm", "-q", path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(*body), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	run(work, "add", "-A")
	run(work, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "-m", "change on "+branch)
	run(work, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	return run(work, "rev-parse", "HEAD")
}

func (s *platformStack) head(branch string) string {
	s.t.Helper()
	out, err := exec.Command("git", "--git-dir="+s.bare, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		s.t.Fatalf("rev-parse %s: %v", branch, err)
	}
	return strings.TrimSpace(string(out))
}

// openAgentRun opens an Engineering Run from source into main, authored by an
// agent: the run an agent opens is its declaration that the work is done.
func (s *platformStack) openAgentRun(source string) *reviewsv1.Run {
	s.t.Helper()
	resp, err := s.reviews.CreateRun(s.asAgent(), &reviewsv1.CreateRunRequest{
		RepoId: s.repoID, Title: "agent work on " + source,
		SourceRef: source, TargetRef: "main",
		AuthorId: uuid.NewString(), AuthorKind: "agent", AgentName: "coder", ModelName: "local",
	})
	if err != nil {
		s.t.Fatalf("CreateRun (agent): %v", err)
	}
	return resp.GetRun()
}

// openRun opens a run authored by the person u.
func (s *platformStack) openRun(u stackUser, source string) *reviewsv1.Run {
	s.t.Helper()
	resp, err := s.reviews.CreateRun(s.as(u), &reviewsv1.CreateRunRequest{
		RepoId: s.repoID, Title: "change on " + source, SourceRef: source, TargetRef: "main",
	})
	if err != nil {
		s.t.Fatalf("CreateRun: %v", err)
	}
	return resp.GetRun()
}

func (s *platformStack) approveReview(u stackUser, runID string) {
	s.t.Helper()
	run, err := s.reviews.GetRun(s.as(u), &reviewsv1.GetRunRequest{Id: runID})
	if err != nil {
		s.t.Fatalf("inspect review run: %v", err)
	}
	head, err := s.git.ListCommits(s.as(u), &gitv1.ListCommitsRequest{Repo: run.GetRun().GetRepoId(), Ref: run.GetRun().GetSourceRef(), Limit: 1})
	if err != nil || len(head.GetCommits()) != 1 {
		s.t.Fatalf("inspect reviewed source: %v (%v)", head, err)
	}
	if _, err := s.reviews.SubmitReview(s.as(u), &reviewsv1.SubmitReviewRequest{RunId: runID, Verdict: "approve", ExpectedSourceSha: head.GetCommits()[0].GetSha()}); err != nil {
		s.t.Fatalf("SubmitReview: %v", err)
	}
}

func (s *platformStack) merge(u stackUser, runID string) (string, error) {
	resp, err := s.reviews.MergeRun(s.as(u), &reviewsv1.MergeRunRequest{RunId: runID, Method: "merge"})
	if err != nil {
		return "", err
	}
	return resp.GetMergeSha(), nil
}

func (s *platformStack) proof(u stackUser, runID string) map[string]*reviewsv1.ProofRecord {
	s.t.Helper()
	resp, err := s.reviews.ListProof(s.as(u), &reviewsv1.ListProofRequest{RunId: runID})
	if err != nil {
		s.t.Fatalf("ListProof: %v", err)
	}
	out := map[string]*reviewsv1.ProofRecord{}
	for _, p := range resp.GetProof() {
		out[p.GetGate()] = p
	}
	return out
}

func str(s string) *string { return &s }
