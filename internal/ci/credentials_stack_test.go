package ci_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/secrets"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// credentialStack is CI's credential path as deployed: the ci-runner pump
// asks the gates service's secret broker over a real gRPC socket, behind the
// real svcauth interceptor, presenting the service token production mints;
// the broker reads real PostgreSQL and resolves the repository's default
// branch through a real git-platform server. Only the runner is a channel,
// because what is asserted is what a runner is handed.
type credentialStack struct {
	t         *testing.T
	pool      *pgxpool.Pool
	store     *ci.Store
	broker    *secrets.Broker
	provider  *credentialProvider
	gatesSrv  *grpc.Server
	org, repo uuid.UUID
	runnerID  uuid.UUID
	// runnerToken is what the runner presents: the platform stores only its
	// hash, and refuses a stream that cannot show the token.
	runnerToken string
	dispatched  chan *civ1.ConnectResponse
	cancel      context.CancelFunc
	pump        *ci.Pump
	dispatcher  *ci.Dispatcher
	redactions  *ci.Redactions
}

const stackHMAC = "credential-stack-hmac"

// gitDirect presents a real gitops.Server as a GitServiceClient in process,
// so the caller's scope reaches it as the interceptor would install it.
// Only GetRepo is used by the broker; anything else panics loudly.
type gitDirect struct {
	gitv1.GitServiceClient
	srv *gitops.Server
}

func (g gitDirect) GetRepo(ctx context.Context, in *gitv1.GetRepoRequest, _ ...grpc.CallOption) (*gitv1.GetRepoResponse, error) {
	return g.srv.GetRepo(ctx, in)
}

func newCredentialStack(t *testing.T) *credentialStack {
	t.Helper()
	url := dbURL(t)
	pool := ciPool(t)
	if err := database.Migrate(url, "secrets", secrets.MigrationsFS); err != nil {
		t.Fatalf("migrate secrets: %v", err)
	}
	if err := database.Migrate(url, "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform: %v", err)
	}
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform_git: %v", err)
	}

	s := &credentialStack{t: t, pool: pool, store: ci.NewStore(pool), org: uuid.New()}
	cleanupOrgRuns(t, pool, s.org)

	gitSrv := gitops.NewGRPCServer(pool, t.TempDir())
	owner := authz.WithScope(context.Background(), authz.Scope{OrgID: s.org, ActorID: uuid.New(), ActorKind: "user"})
	repo, err := gitSrv.CreateRepo(owner, &gitv1.CreateRepoRequest{Name: "svc-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	s.repo = uuid.MustParse(repo.GetRepo().GetId())

	s.setupProvider(t)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM secrets.secret_values WHERE org_id = $1", s.org)
		pool.Exec(context.Background(), "DELETE FROM secrets.secret_leases WHERE org_id = $1", s.org)
	})
	gatesServer := gates.NewGRPCServer(nil, nil, s.broker, nil)
	gatesServer.DefaultBranch = gates.DefaultBranchFromGit(gitDirect{srv: gitSrv})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.gatesSrv = grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, stackHMAC)))
	gatesv1.RegisterGatesServiceServer(s.gatesSrv, gatesServer)
	go func() { _ = s.gatesSrv.Serve(lis) }()
	t.Cleanup(s.gatesSrv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gates: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	dispatcher := ci.NewDispatcher(s.store)
	s.dispatcher = dispatcher
	s.redactions = ci.NewRedactions()
	s.pump = ci.NewPump(s.store, dispatcher, "http://git.test", stackHMAC)
	s.pump.Redactions = s.redactions
	s.pump.Credentials = &ci.GatesBroker{Gates: gatesv1.NewGatesServiceClient(conn), HMACSecret: stackHMAC}

	rawToken := []byte(uuid.NewString())
	tokenHash := sha256.Sum256(rawToken)
	s.runnerToken = hex.EncodeToString(rawToken)
	s.runnerID, err = s.store.RegisterRunner(context.Background(), s.org, "cred-runner", []string{"linux"}, tokenHash[:])
	if err != nil {
		t.Fatalf("RegisterRunner: %v", err)
	}
	s.dispatched = make(chan *civ1.ConnectResponse, 16)
	dispatcher.Register(s.runnerID, []string{"linux"}, s.dispatched)
	return s
}

func (s *credentialStack) start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.t.Cleanup(cancel)
	go s.pump.Run(ctx)
}

// job creates a one-job run at ref.
func (s *credentialStack) job(ref, name, env string, secretNames ...string) uuid.UUID {
	s.t.Helper()
	ctx := context.Background()
	run, _, err := s.store.CreateRun(ctx, ci.Run{
		OrgID: s.org, RepoID: s.repo, RepoName: "svc",
		CommitSHA: uuid.NewString(), Ref: ref,
	})
	if err != nil {
		s.t.Fatalf("CreateRun: %v", err)
	}
	j, err := s.store.CreateJob(ctx, ci.WorkflowJob{
		RunID: run.ID, Name: name, RunCmd: "echo " + name,
		Environment: env, Secrets: secretNames,
	})
	if err != nil {
		s.t.Fatalf("CreateJob: %v", err)
	}
	return j.ID
}

// waitDispatched returns the dispatched jobs, keyed by id, seen within d.
func (s *credentialStack) waitDispatched(d time.Duration, want int) map[string]*civ1.ConnectResponse {
	got := map[string]*civ1.ConnectResponse{}
	deadline := time.After(d)
	for len(got) < want {
		select {
		case j := <-s.dispatched:
			got[j.GetJobId()] = j
		case <-deadline:
			return got
		}
	}
	return got
}

func (s *credentialStack) jobState(id uuid.UUID) ci.WorkflowJob {
	s.t.Helper()
	j, err := s.store.GetJob(context.Background(), id)
	if err != nil {
		s.t.Fatalf("GetJob: %v", err)
	}
	return j
}

// secretValue assembles a credential at runtime, so no literal that looks
// like one is committed.
func secretValue(tag string) string {
	return strings.Join([]string{"nf", tag, uuid.NewString()}, "-")
}

// TestShortLivedCredential (S-12): a CI job declaring a secret receives it
// from the broker at dispatch, through a lease that expires on schedule and
// is spent once redeemed; a staging job asking for a production-only secret
// is refused and fails saying why; and a production job gets a production
// credential only for a commit on the repository's default branch.
func TestShortLivedCredential(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	staging := secretValue("staging")
	prod := secretValue("prod")
	if err := s.putDynamicSecret("DEPLOY_TOKEN", "staging", staging); err != nil {
		t.Fatal(err)
	}
	if err := s.putDynamicSecret("PROD_KEY", "production", prod); err != nil {
		t.Fatal(err)
	}

	deploy := s.job("refs/heads/feature", "deploy-staging", "staging", "DEPLOY_TOKEN")
	sneaky := s.job("refs/heads/feature", "staging-wants-prod", "staging", "PROD_KEY")
	branchProd := s.job("refs/heads/feature", "prod-from-branch", "production", "PROD_KEY")
	mainProd := s.job("refs/heads/main", "prod-from-main", "production", "PROD_KEY")
	s.start()

	got := s.waitDispatched(25*time.Second, 2)
	d, ok := got[deploy.String()]
	if !ok {
		t.Fatalf("the staging job was not dispatched; got %v, job %+v", got, s.jobState(deploy))
	}
	if d.GetSecretEnv()["DEPLOY_TOKEN"] != staging {
		t.Fatalf("the staging job did not receive its credential: %v", d.GetSecretEnv())
	}
	if _, inEnv := d.GetEnv()["DEPLOY_TOKEN"]; inEnv {
		t.Fatal("the credential travels in env, where it is not redacted or kept out of the pod spec")
	}
	m, ok := got[mainProd.String()]
	if !ok || m.GetSecretEnv()["PROD_KEY"] != prod {
		t.Fatalf("a production job on the default branch did not receive the production credential: %v", got)
	}
	for _, refused := range []uuid.UUID{sneaky, branchProd} {
		if _, sent := got[refused.String()]; sent {
			t.Fatalf("job %s was dispatched with a credential it must be refused", refused)
		}
	}
	// Refusals are failures that say why, not silence.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && (s.jobState(sneaky).Status != "failure" || s.jobState(branchProd).Status != "failure") {
		time.Sleep(500 * time.Millisecond)
	}
	if j := s.jobState(sneaky); j.Status != "failure" || !strings.Contains(j.Detail, "production") {
		t.Fatalf("staging job asking for a production secret: %s %q, want failure naming production", j.Status, j.Detail)
	}
	if j := s.jobState(branchProd); j.Status != "failure" || !strings.Contains(j.Detail, "default branch") {
		t.Fatalf("production job off the default branch: %s %q, want failure naming the default branch", j.Status, j.Detail)
	}

	scoped := authz.WithScope(ctx, authz.Scope{OrgID: s.org, ActorKind: "service"})
	leases, err := s.broker.ListLeases(scoped, s.org)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, l := range leases {
		if l.RunID != deploy {
			continue
		}
		found = true
		if l.State != "spent" {
			t.Fatalf("the job's lease is %q after it was redeemed, want spent", l.State)
		}
		if ttl := time.Until(l.ExpiresAt); ttl <= 0 || ttl > 16*time.Minute {
			t.Fatalf("lease expires in %s, want a short-lived lease of at most 15 minutes", ttl)
		}
	}
	if !found {
		t.Fatalf("no lease was recorded for the staging job; leases: %+v", leases)
	}
}

// TestBrokerDownFailsClosed (S-12): with the broker unreachable, a job that
// declares a secret is not dispatched — it stays pending, blocked with the
// reason, so it runs once the broker returns — while a job declaring no
// secret still runs.
func TestBrokerDownFailsClosed(t *testing.T) {
	s := newCredentialStack(t)
	s.gatesSrv.Stop()

	needs := s.job("refs/heads/main", "needs-secret", "staging", "DEPLOY_TOKEN")
	free := s.job("refs/heads/main", "no-secret", "staging")
	s.start()

	got := s.waitDispatched(20*time.Second, 2)
	if _, ok := got[free.String()]; !ok {
		t.Fatalf("a credential-free job was held up by the broker being down; job %+v", s.jobState(free))
	}
	if _, ok := got[needs.String()]; ok {
		t.Fatal("a job that needs a credential was dispatched while the broker was down")
	}
	j := s.jobState(needs)
	if j.Status != "pending" || !strings.Contains(j.Detail, "broker") {
		t.Fatalf("credential-needing job: %s %q, want pending and blocked naming the broker", j.Status, j.Detail)
	}
}

func (s *credentialStack) secretContext() context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: s.org, ActorKind: "service"})
}
