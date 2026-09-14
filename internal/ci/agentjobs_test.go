package ci_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/ci"
)

// agentService stands in for agent-runtime's gRPC surface: it records what it
// was asked to start and reports whatever state the test sets. Embedding the
// client interface makes any other method a nil-pointer panic, so a worker
// that starts calling something unexpected fails loudly.
type agentService struct {
	agentsv1.AgentServiceClient
	mu      sync.Mutex
	agents  []*agentsv1.Agent
	started []*agentsv1.StartRunRequest
	orgs    []string
	state   string
	runID   string
}

func (a *agentService) ListAgents(ctx context.Context, _ *agentsv1.ListAgentsRequest, _ ...grpc.CallOption) (*agentsv1.ListAgentsResponse, error) {
	a.recordOrg(ctx)
	return &agentsv1.ListAgentsResponse{Agents: a.agents}, nil
}

func (a *agentService) StartRun(ctx context.Context, req *agentsv1.StartRunRequest, _ ...grpc.CallOption) (*agentsv1.StartRunResponse, error) {
	a.recordOrg(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = append(a.started, req)
	a.state = "running"
	return &agentsv1.StartRunResponse{Run: &agentsv1.Run{Id: a.runID, State: "queued"}}, nil
}

func (a *agentService) GetRun(ctx context.Context, req *agentsv1.GetRunRequest, _ ...grpc.CallOption) (*agentsv1.GetRunResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if req.GetId() != a.runID {
		return nil, errors.New("unknown run")
	}
	return &agentsv1.GetRunResponse{Run: &agentsv1.Run{Id: a.runID, State: a.state}}, nil
}

func (a *agentService) recordOrg(ctx context.Context) {
	md, _ := metadata.FromOutgoingContext(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.orgs = append(a.orgs, strings.Join(md.Get("x-novaforge-org"), ","))
}

type workService struct {
	workv1.WorkServiceClient
	created []*workv1.CreateItemRequest
}

func (w *workService) CreateItem(_ context.Context, req *workv1.CreateItemRequest, _ ...grpc.CallOption) (*workv1.CreateItemResponse, error) {
	w.created = append(w.created, req)
	return &workv1.CreateItemResponse{Item: &workv1.WorkItem{Key: "NF-7", RepoId: req.GetRepoId()}}, nil
}

// agentJobFixture schedules a run with a shell job "test" and an agent job
// "security-review" that needs it.
func agentJobFixture(t *testing.T, store *ci.Store, orgID, triggeredBy uuid.UUID) (ci.Run, ci.WorkflowJob, ci.WorkflowJob) {
	t.Helper()
	ctx := context.Background()
	run, _, err := store.CreateRun(ctx, ci.Run{
		OrgID: orgID, RepoID: uuid.New(), RepoName: "widgets",
		CommitSHA: "0123456789abcdef", Ref: "refs/heads/main", TriggeredBy: triggeredBy,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	shell, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "test", RunCmd: "go test ./..."})
	if err != nil {
		t.Fatalf("CreateJob test: %v", err)
	}
	agent, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "security-review", AgentRole: "security", Needs: []string{"test"}})
	if err != nil {
		t.Fatalf("CreateJob security-review: %v", err)
	}
	return run, shell, agent
}

// TestAgentCIJob is the spec's S-6: a CI job declared with an agent role
// executes as an agent run and reports status like any other job. Before,
// a runner was handed the job, ran an empty shell command, and reported
// success without any agent being asked.
func TestAgentCIJob(t *testing.T) {
	pool := ciPoolExclusive(t)
	store := ci.NewStore(pool)
	ctx := context.Background()
	orgID, sponsor := uuid.New(), uuid.New()
	t.Cleanup(func() { cleanupOrgRuns(t, pool, orgID) })
	run, shell, agentJob := agentJobFixture(t, store, orgID, sponsor)

	// A runner must never be handed the agent job, even once its needs are met.
	runnerID, err := store.RegisterRunner(ctx, orgID, "agent-job-test", []string{"linux"}, []byte("hash-"+uuid.NewString()))
	if err != nil {
		t.Fatalf("RegisterRunner: %v", err)
	}
	claimed, _, err := store.ClaimForDispatch(ctx, runnerID, nil)
	if err != nil || claimed.JobID != shell.ID {
		t.Fatalf("runner claim = %v, %v; want the shell job %s", claimed.JobID, err, shell.ID)
	}

	agents := &agentService{
		runID:  uuid.NewString(),
		agents: []*agentsv1.Agent{{Id: uuid.NewString(), Role: "security", Enabled: true}},
	}
	work := &workService{}
	worker := &ci.AgentJobs{Store: store, Agents: agents, Work: work, HMACSecret: "test-secret"}

	// Blocked on "test": nothing starts.
	if err := worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(agents.started) != 0 {
		t.Fatalf("agent run started before the job's needs succeeded")
	}
	if err := store.SetJobStatus(ctx, shell.ID, "success", ""); err != nil {
		t.Fatalf("SetJobStatus: %v", err)
	}
	if _, _, err := store.ClaimForDispatch(ctx, runnerID, nil); !errors.Is(err, ci.ErrNoClaimableJob) {
		t.Fatalf("runner claimed the agent job: err = %v", err)
	}

	if err := worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(agents.started) != 1 {
		t.Fatalf("agent runs started = %d, want 1", len(agents.started))
	}
	req := agents.started[0]
	if req.GetSponsorId() != sponsor.String() || req.GetAgentId() != agents.agents[0].GetId() || req.GetWorkItemKey() != "NF-7" {
		t.Fatalf("StartRun = %+v", req)
	}
	for _, org := range agents.orgs {
		if org != orgID.String() {
			t.Fatalf("a call named organization %q, want %s", org, orgID)
		}
	}
	if len(work.created) != 1 || len(work.created[0].GetAcceptance()) == 0 {
		t.Fatalf("work item brief = %+v", work.created)
	}
	if got := jobStatus(t, store, agentJob.ID); got != "running" {
		t.Fatalf("job status while the agent works = %q, want running", got)
	}

	agents.state = "failed"
	if err := worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	job, err := store.GetJob(ctx, agentJob.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != "failure" || !strings.Contains(job.Detail, agents.runID) {
		t.Fatalf("job = %q %q, want failure naming the agent run", job.Status, job.Detail)
	}
	got, err := store.GetRun(scopedCtx(orgID), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != "failure" {
		t.Fatalf("run status = %q, want failure: an agent's failed review fails the run", got.Status)
	}
}

// TestAgentCIJobWithoutSponsorFails pins that a run no person triggered (a
// push by an agent or a service) fails its agent job with the reason, rather
// than starting an agent under nobody's name or sitting pending forever.
func TestAgentCIJobWithoutSponsorFails(t *testing.T) {
	pool := ciPoolExclusive(t)
	store := ci.NewStore(pool)
	ctx := context.Background()
	orgID := uuid.New()
	t.Cleanup(func() { cleanupOrgRuns(t, pool, orgID) })
	_, shell, agentJob := agentJobFixture(t, store, orgID, uuid.Nil)
	if err := store.SetJobStatus(ctx, shell.ID, "success", ""); err != nil {
		t.Fatal(err)
	}

	agents := &agentService{runID: uuid.NewString(), agents: []*agentsv1.Agent{{Id: uuid.NewString(), Role: "security", Enabled: true}}}
	worker := &ci.AgentJobs{Store: store, Agents: agents, Work: &workService{}, HMACSecret: "test-secret"}
	if err := worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	job, err := store.GetJob(ctx, agentJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failure" || !strings.Contains(job.Detail, "sponsor") || len(agents.started) != 0 {
		t.Fatalf("job = %q %q, started = %d; want a failure explaining there is no sponsor", job.Status, job.Detail, len(agents.started))
	}
}

// TestAgentCIJobWithNoAgentForRoleFails pins the message a workflow author
// needs when their organization has no agent with the role a job names.
func TestAgentCIJobWithNoAgentForRoleFails(t *testing.T) {
	pool := ciPoolExclusive(t)
	store := ci.NewStore(pool)
	ctx := context.Background()
	orgID := uuid.New()
	t.Cleanup(func() { cleanupOrgRuns(t, pool, orgID) })
	_, shell, agentJob := agentJobFixture(t, store, orgID, uuid.New())
	if err := store.SetJobStatus(ctx, shell.ID, "success", ""); err != nil {
		t.Fatal(err)
	}

	agents := &agentService{runID: uuid.NewString(), agents: []*agentsv1.Agent{{Id: uuid.NewString(), Role: "qa", Enabled: true}}}
	worker := &ci.AgentJobs{Store: store, Agents: agents, Work: &workService{}, HMACSecret: "test-secret"}
	if err := worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	job, err := store.GetJob(ctx, agentJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failure" || !strings.Contains(job.Detail, `role "security"`) {
		t.Fatalf("job = %q %q, want a failure naming the missing role", job.Status, job.Detail)
	}
}

func jobStatus(t *testing.T, store *ci.Store, id uuid.UUID) string {
	t.Helper()
	job, err := store.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	return job.Status
}
