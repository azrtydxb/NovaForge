package agents_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
)

// stubWorkClient serves a fixed Work Item for GetItem and panics on any
// other method — agents.GRPCServer only ever calls GetItem.
type stubWorkClient struct {
	workv1.WorkServiceClient
	item *workv1.WorkItem
}

func (s *stubWorkClient) GetItem(ctx context.Context, in *workv1.GetItemRequest, opts ...grpc.CallOption) (*workv1.GetItemResponse, error) {
	if s.item == nil || in.GetKey() != s.item.GetKey() {
		return nil, status.Error(codes.NotFound, "work item not found")
	}
	return &workv1.GetItemResponse{Item: s.item}, nil
}

func grantsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform (capability) schema: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func agentsRedis(t *testing.T) *redis.Client {
	t.Helper()
	u := os.Getenv("TEST_REDIS_URL")
	if u == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(u)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func newGRPCServer(t *testing.T, work *stubWorkClient) (*agents.GRPCServer, *agents.Store) {
	t.Helper()
	store := newStore(t)
	grants := capability.NewStore(grantsPool(t))
	rdb := agentsRedis(t)
	store.WorkClaims = &workClaimsFixture{item: work.item}
	return agents.NewGRPCServer(store, ownerFixture{grants}, rdb, work, func(context.Context, agents.Run) {}), store
}

// TestStartRunIssuesScopedGrant starts a run for Work Item "NF-1" and
// asserts the issued grant's WriteBranch is exactly "agents/NF-1/" and that
// secrets_prod and deploy_prod are both false — an agent run must never be
// handed broader access than its own scoped workspace branch.
func TestStartRunIssuesScopedGrant(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()

	work := &stubWorkClient{item: &workv1.WorkItem{
		Id: uuid.New().String(), Key: "NF-1", RepoId: repoID.String(), State: "open",
	}}
	srv, store := newGRPCServer(t, work)
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)

	resp, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{
		AgentId:     agent.ID.String(),
		RepoId:      repoID.String(),
		WorkItemKey: "NF-1",
		SponsorId:   actorOf(ctx),
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// The grant allows a prefix; the run must name a real branch inside it.
	// Recording the prefix itself as the branch gave the agent a ref it
	// could not push (a ref may not end in a slash) and refused every
	// sensible alternative it tried.
	branch := resp.GetRun().GetBranch()
	if branch == "agents/NF-1/" || strings.HasSuffix(branch, "/") {
		t.Fatalf("StartRun recorded the grant's prefix %q as the run's branch, which is not a usable ref", branch)
	}
	if !strings.HasPrefix(branch, "agents/NF-1/") {
		t.Fatalf("run branch %q is outside its grant's prefix %q", branch, "agents/NF-1/")
	}

	grantID, err := uuid.Parse(resp.GetRun().GetGrantId())
	if err != nil {
		t.Fatalf("parse grant id: %v", err)
	}
	grant, err := capability.NewStore(grantsPool(t)).Resolve(scopedCtx(orgID), grantID)
	if err != nil {
		t.Fatalf("resolve grant: %v", err)
	}
	if grant.SecretsProd {
		t.Fatal("issued grant has SecretsProd = true, want false")
	}
	if grant.DeployProd {
		t.Fatal("issued grant has DeployProd = true, want false")
	}
	if grant.WriteBranch != "agents/NF-1/" {
		t.Fatalf("issued grant WriteBranch = %q, want %q", grant.WriteBranch, "agents/NF-1/")
	}
}

// TestStartRunDeniedWithoutSponsor asserts a start with no human sponsor
// returns PermissionDenied: an agent run must always be traceable to a
// human who is accountable for it.
func TestStartRunDeniedWithoutSponsor(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	work := &stubWorkClient{item: &workv1.WorkItem{
		Id: uuid.New().String(), Key: "NF-2", RepoId: repoID.String(), State: "open",
	}}
	srv, store := newGRPCServer(t, work)
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)

	_, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{
		AgentId:     agent.ID.String(),
		RepoId:      repoID.String(),
		WorkItemKey: "NF-2",
		SponsorId:   "",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("StartRun with no sponsor: code = %v, want PermissionDenied", status.Code(err))
	}
}

// TestStreamRunEventsDeliversStateChanges asserts a state change from
// queued to running (the transition StartRun always drives in the
// background once the run is created) is delivered on the run's event
// stream.
func TestStreamRunEventsDeliversStateChanges(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	work := &stubWorkClient{item: &workv1.WorkItem{
		Id: uuid.New().String(), Key: "NF-3", RepoId: repoID.String(), State: "open",
	}}
	srv, store := newGRPCServer(t, work)
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)

	started, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{
		AgentId:     agent.ID.String(),
		RepoId:      repoID.String(),
		WorkItemKey: "NF-3",
		SponsorId:   actorOf(ctx),
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	runID := started.GetRun().GetId()

	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fake := &fakeStreamServer{ctx: streamCtx, recv: make(chan *agentsv1.StreamRunEventsResponse, 16)}

	go func() {
		_ = srv.StreamRunEvents(&agentsv1.StreamRunEventsRequest{RunId: runID}, fake)
	}()

	var sawQueuedToRunning bool
	deadline := time.After(4 * time.Second)
	for !sawQueuedToRunning {
		select {
		case evt := <-fake.recv:
			sc := evt.GetStateChange()
			if sc != nil && sc.GetFromState() == "queued" && sc.GetToState() == "running" {
				sawQueuedToRunning = true
			}
		case <-deadline:
			t.Fatal("did not observe a queued -> running state change on the stream")
		}
	}
}

// fakeStreamServer implements agentsv1.AgentService_StreamRunEventsServer
// enough for GRPCServer.StreamRunEvents to drive: it captures every sent
// message on a channel and exposes the context the RPC should honor.
type fakeStreamServer struct {
	grpc.ServerStream
	ctx  context.Context
	recv chan *agentsv1.StreamRunEventsResponse
}

func (f *fakeStreamServer) Send(resp *agentsv1.StreamRunEventsResponse) error {
	f.recv <- resp
	return nil
}

func (f *fakeStreamServer) SendHeader(metadata.MD) error { return nil }

func (f *fakeStreamServer) Context() context.Context { return f.ctx }

// TestRunBranchIsUsableUnderItsGrant pins a defect that made every agent
// commit impossible. The capability grant allows a PREFIX — everything
// under "agents/NF-1/" — and the run recorded that prefix as its branch. A
// git ref may not end in a slash, so the agent could not use what it was
// given, and the obvious alternative ("agents/NF-1") falls outside the
// prefix and is refused. The run must name a real branch inside its own
// grant.
func TestRunBranchIsUsableUnderItsGrant(t *testing.T) {
	grant := capability.Grant{WriteBranch: "agents/NF-1/"}
	branch := agents.RunBranch(grant.WriteBranch)

	if branch == "" || strings.HasSuffix(branch, "/") {
		t.Fatalf("run branch %q is not a usable git ref", branch)
	}
	if err := capability.CanWriteRef(grant, "refs/heads/"+branch); err != nil {
		t.Fatalf("the run's own branch is refused by its own grant: %v", err)
	}
}

// TestRunBranchLeavesAConcretePrefixAlone pins that a grant already naming
// one branch is not turned into a different one.
func TestRunBranchLeavesAConcretePrefixAlone(t *testing.T) {
	if got := agents.RunBranch("agents/NF-2/work"); got != "agents/NF-2/work" {
		t.Fatalf("want the branch unchanged, got %q", got)
	}
}
