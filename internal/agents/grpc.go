package agents

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/events"
)

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

// defaultGrantTTL is how long a Start-issued capability grant remains
// active. A run that outlives it is expected to have already finished
// (WallclockLimit bounds every run well below this).
const defaultGrantTTL = 24 * time.Hour

// ExecuteFunc takes over a run once it has been created and issued its
// grant: provisioning its isolated workspace, running the agent loop, and
// driving it to a terminal state. It is invoked in a fresh background
// context (a running agent must outlive the StartRun request that
// launched it) after the run's state has already moved to "running". When
// nil, StartRun still transitions the run from queued to running — the
// state change a caller subscribed via StreamRunEvents can rely on
// seeing — but nothing then executes it further, which is the same
// nil-safe degrade every other service in this codebase uses for an
// optional dependency the caller has not wired up (compare graph.Assemble).
type ExecuteFunc func(ctx context.Context, run Run)

// GRPCServer implements agentsv1.AgentServiceServer. Every method derives
// the caller's organization from authz.FromContext and passes it as an
// explicit predicate to every query — never from a field of the request
// message, so a client cannot simply name a different org and be believed.
type GRPCServer struct {
	agentsv1.UnimplementedAgentServiceServer

	Store   *Store
	Grants  *capability.Store
	RDB     *redis.Client
	Work    workv1.WorkServiceClient
	Execute ExecuteFunc

	// Audit backs ListToolCalls: the audited record of what a run did. It is
	// nil in a deployment that records none, and ListToolCalls says so rather
	// than answering with an empty list, which would read as "it did nothing".
	Audit *AuditLog

	// Price is the token price of the model runs execute on, from
	// AI_MODEL_PRICES; nil when the deployment prices none, in which case
	// StartRun refuses a cost limit it could never enforce.
	Price *TokenPrice

	// executing holds the cancel function of every run this process is
	// executing, so CancelRun can abort an in-flight model or tool call
	// rather than only writing a state the loop would not see until its next
	// step. A run executing on another replica is not in this map; that loop
	// observes cancellation by reading its run's state at each step boundary
	// (agentrun.Loop), which is why the state write stays authoritative.
	executingMu sync.Mutex
	executing   map[uuid.UUID]context.CancelFunc
}

// NewGRPCServer wraps the given dependencies as an agentsv1.AgentServiceServer.
func NewGRPCServer(store *Store, grants *capability.Store, rdb *redis.Client, work workv1.WorkServiceClient, execute ExecuteFunc) *GRPCServer {
	return &GRPCServer{Store: store, Grants: grants, RDB: rdb, Work: work, Execute: execute}
}

func callerOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	return scope.OrgID, nil
}

func parseUUID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "invalid %s %q: %v", field, raw, err)
	}
	return id, nil
}

func toProtoAgent(a Agent) *agentsv1.Agent {
	return &agentsv1.Agent{
		Id:       a.ID.String(),
		OrgId:    a.OrgID.String(),
		Name:     a.Name,
		Role:     a.Role,
		ModelRef: a.ModelRef,
		Enabled:  a.Enabled,
	}
}

func toProtoRun(r Run) *agentsv1.Run {
	out := &agentsv1.Run{
		Id:                    r.ID.String(),
		OrgId:                 r.OrgID.String(),
		AgentId:               r.AgentID.String(),
		Branch:                r.Branch,
		State:                 r.State,
		WallclockLimitSeconds: int64(r.WallclockLimit / time.Second),
		TokenLimit:            r.TokenLimit,
		CostLimitMicros:       r.CostLimitMicros,
		TokensUsed:            r.TokensUsed,
		CostUsedMicros:        r.CostUsedMicros,
		EndReason:             r.EndReason,
	}
	if r.WorkItemID != uuid.Nil {
		out.WorkItemId = r.WorkItemID.String()
	}
	if r.SponsorID != uuid.Nil {
		out.SponsorId = r.SponsorID.String()
	}
	if r.GrantID != uuid.Nil {
		out.GrantId = r.GrantID.String()
	}
	if !r.StartedAt.IsZero() {
		out.StartedAt = r.StartedAt.Format(rfc3339)
	}
	if r.EndedAt != nil {
		out.EndedAt = r.EndedAt.Format(rfc3339)
	}
	return out
}

// CreateAgent registers a new agent identity within the caller's organization.
func (g *GRPCServer) CreateAgent(ctx context.Context, req *agentsv1.CreateAgentRequest) (*agentsv1.CreateAgentResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	created, err := g.Store.CreateAgent(ctx, Agent{
		OrgID:    orgID,
		Name:     req.GetName(),
		Role:     req.GetRole(),
		ModelRef: req.GetModelRef(),
		Enabled:  req.GetEnabled(),
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "create agent: %v", err)
	}
	return &agentsv1.CreateAgentResponse{Agent: toProtoAgent(created)}, nil
}

// ListAgents lists every agent within the caller's organization.
func (g *GRPCServer) ListAgents(ctx context.Context, req *agentsv1.ListAgentsRequest) (*agentsv1.ListAgentsResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	list, err := g.Store.ListAgents(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list agents: %v", err)
	}
	out := make([]*agentsv1.Agent, len(list))
	for i, a := range list {
		out[i] = toProtoAgent(a)
	}
	return &agentsv1.ListAgentsResponse{Agents: out}, nil
}

// StartRun starts a new agent run against a Work Item. sponsor_id must name
// a human member of the organization: a run started with no sponsor is
// refused with PermissionDenied. It issues a capability grant scoped to
// exactly the Work Item's agent branch, with no secret or deploy access,
// creates the run "queued", publishes that state change, then transitions
// it to "running" and hands it to Execute (when configured) in the
// background — StartRun itself returns as soon as the run exists.
func (g *GRPCServer) StartRun(ctx context.Context, req *agentsv1.StartRunRequest) (*agentsv1.StartRunResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetSponsorId() == "" {
		return nil, status.Error(codes.PermissionDenied, "an agent run requires a human sponsor")
	}
	sponsorID, err := parseUUID("sponsor_id", req.GetSponsorId())
	if err != nil {
		return nil, err
	}
	agentID, err := parseUUID("agent_id", req.GetAgentId())
	if err != nil {
		return nil, err
	}
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if req.GetWorkItemKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "work_item_key is required")
	}
	if g.Work == nil {
		return nil, status.Error(codes.Internal, "work service is not configured")
	}
	itemResp, err := g.Work.GetItem(ctx, &workv1.GetItemRequest{Key: req.GetWorkItemKey()})
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "resolve work item %q: %v", req.GetWorkItemKey(), err)
	}
	workItemID, err := parseUUID("work_item.id", itemResp.GetItem().GetId())
	if err != nil {
		return nil, err
	}
	if itemResp.GetItem().GetRepoId() != req.GetRepoId() {
		return nil, status.Errorf(codes.InvalidArgument, "work item %q belongs to a different repository than repo_id %q", req.GetWorkItemKey(), req.GetRepoId())
	}
	// A maintenance proposal is executed only once a person has approved it.
	// This is the one place every run starts — a person's click, the swarm,
	// a CI agent job — so refusing here holds for all of them, before any
	// grant is issued.
	if req.GetCostLimitMicros() < 0 || req.GetTokenLimit() < 0 || req.GetWallclockLimitSeconds() < 0 {
		return nil, status.Error(codes.InvalidArgument, "a run's limits may not be negative")
	}
	// A cost limit is enforced only against a token price. With none
	// configured no cost ever accrues, so accepting the limit would record a
	// bound that can never be reached; refusing says so to the person asking.
	if req.GetCostLimitMicros() > 0 && g.Price == nil {
		return nil, status.Error(codes.InvalidArgument, "this deployment prices no model tokens, so a cost limit could never be reached; "+
			"start the run without cost_limit_micros, or have the operator set the model's price in AI_MODEL_PRICES")
	}
	if itemResp.GetItem().GetAwaitingApproval() {
		return nil, status.Errorf(codes.FailedPrecondition, "work item %q is a maintenance proposal awaiting approval; approve it before starting an agent on it", req.GetWorkItemKey())
	}

	grant := capability.Grant{
		OrgID:         orgID,
		SubjectID:     agentID,
		SubjectKind:   "agent",
		RepoRead:      true,
		WriteBranch:   "agents/" + req.GetWorkItemKey() + "/",
		SecretsProd:   false,
		DeployStaging: false,
		DeployProd:    false,
		ExpiresAt:     time.Now().Add(defaultGrantTTL),
	}
	issued, err := g.Grants.Issue(ctx, grant)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issue capability grant: %v", err)
	}

	run, err := g.Store.CreateRun(ctx, Run{
		OrgID:           orgID,
		RepoID:          repoID,
		AgentID:         agentID,
		WorkItemID:      workItemID,
		SponsorID:       sponsorID,
		GrantID:         issued.ID,
		Branch:          RunBranch(issued.WriteBranch),
		WallclockLimit:  time.Duration(req.GetWallclockLimitSeconds()) * time.Second,
		TokenLimit:      req.GetTokenLimit(),
		CostLimitMicros: req.GetCostLimitMicros(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create run: %v", err)
	}

	g.publishStateChange(run.ID, "", "queued")

	go g.driveToRunning(run)

	return &agentsv1.StartRunResponse{Run: toProtoRun(run)}, nil
}

// driveToRunning transitions a freshly created run from queued to running
// and publishes that change, then hands off to Execute if one is
// configured. It runs in the background, detached from the StartRun
// request's context, since an agent run outlives the RPC that started it.
func (g *GRPCServer) driveToRunning(run Run) {
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: run.OrgID, ActorID: run.AgentID, ActorKind: "agent"})
	if err := g.Store.SetRunState(ctx, run.ID, "running"); err != nil {
		return
	}
	g.publishStateChange(run.ID, "queued", "running")
	run.State = "running"

	if g.Execute != nil {
		runCtx, cancel := context.WithCancel(ctx)
		g.track(run.ID, cancel)
		defer g.untrack(run.ID)
		defer cancel()
		g.Execute(runCtx, run)
	}
}

func (g *GRPCServer) track(id uuid.UUID, cancel context.CancelFunc) {
	g.executingMu.Lock()
	defer g.executingMu.Unlock()
	if g.executing == nil {
		g.executing = make(map[uuid.UUID]context.CancelFunc)
	}
	g.executing[id] = cancel
}

func (g *GRPCServer) untrack(id uuid.UUID) {
	g.executingMu.Lock()
	defer g.executingMu.Unlock()
	delete(g.executing, id)
}

// abort cancels run id's execution context if this process is executing it.
func (g *GRPCServer) abort(id uuid.UUID) {
	g.executingMu.Lock()
	cancel, ok := g.executing[id]
	g.executingMu.Unlock()
	if ok {
		cancel()
	}
}

func (g *GRPCServer) publishStateChange(runID uuid.UUID, from, to string) {
	publishStateChange(g.RDB, runID, from, to)
}

// GetRun looks up a run by id within the caller's organization.
func (g *GRPCServer) GetRun(ctx context.Context, req *agentsv1.GetRunRequest) (*agentsv1.GetRunResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	run, err := g.Store.GetRun(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "get run: %v", err)
	}
	return &agentsv1.GetRunResponse{Run: toProtoRun(run)}, nil
}

// CancelRun transitions a run to cancelled, within the caller's organization,
// and stops it. The state is written first: it is what a loop on any replica
// checks, and the store's state machine refuses to move a cancelled run to
// any other state, so a loop finishing concurrently cannot overwrite it with
// "succeeded". Only then is the local execution context cancelled.
func (g *GRPCServer) CancelRun(ctx context.Context, req *agentsv1.CancelRunRequest) (*agentsv1.CancelRunResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	run, err := g.Store.GetRun(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "get run: %v", err)
	}
	if err := g.Store.SetRunState(ctx, id, "cancelled"); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cancel run: %v", err)
	}
	g.abort(id)
	g.publishStateChange(id, run.State, "cancelled")
	return &agentsv1.CancelRunResponse{Ok: true}, nil
}

// StreamRunEvents tails events.StreamAgentEvents from its beginning,
// forwarding every message for the requested run until the client
// disconnects or ctx is cancelled. Reading from the beginning (rather than
// only new messages) means a client that subscribes immediately after
// StartRun returns still sees the queued and running state changes that
// were published just before it connected.
func (g *GRPCServer) StreamRunEvents(req *agentsv1.StreamRunEventsRequest, stream agentsv1.AgentService_StreamRunEventsServer) error {
	ctx := stream.Context()
	if _, err := callerOrg(ctx); err != nil {
		return err
	}
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return err
	}
	if g.RDB == nil {
		return status.Error(codes.Unavailable, "event stream is not configured")
	}

	lastID := "0"
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		res, err := g.RDB.XRead(ctx, &redis.XReadArgs{
			Streams: []string{events.StreamAgentEvents, lastID},
			Block:   2 * time.Second,
			Count:   100,
		}).Result()
		if err != nil {
			if err == redis.Nil || ctx.Err() != nil {
				continue
			}
			return status.Errorf(codes.Internal, "read event stream: %v", err)
		}
		for _, s := range res {
			for _, msg := range s.Messages {
				lastID = msg.ID
				evt, ok := decodeAgentEvent(msg.Values)
				if !ok || evt.RunID != runID {
					continue
				}
				resp := &agentsv1.StreamRunEventsResponse{
					RunId: evt.RunID.String(),
					At:    evt.At.Format(rfc3339),
				}
				switch evt.Type {
				case "tool_call":
					resp.Payload = &agentsv1.StreamRunEventsResponse_ToolCall{
						ToolCall: &agentsv1.ToolCall{Tool: evt.Tool, Outcome: evt.Outcome},
					}
				default:
					resp.Payload = &agentsv1.StreamRunEventsResponse_StateChange{
						StateChange: &agentsv1.StateChange{FromState: evt.FromState, ToState: evt.ToState},
					}
				}
				if err := stream.Send(resp); err != nil {
					return err
				}
			}
		}
	}
}

func decodeAgentEvent(values map[string]interface{}) (events.AgentEvent, bool) {
	raw, ok := values["data"].(string)
	if !ok {
		return events.AgentEvent{}, false
	}
	var evt events.AgentEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		return events.AgentEvent{}, false
	}
	return evt, true
}

// runBranchLeaf is the branch a run works on beneath its grant's prefix.
const runBranchLeaf = "work"

// RunBranch turns a grant's write-branch PREFIX into a concrete branch name
// the run can actually push.
//
// The grant allows everything under "agents/NF-1/", and the run used to
// record that prefix as its branch. An agent told its branch was
// "agents/NF-1/" cannot use it — a ref may not end in a slash — and every
// sensible thing it tried instead ("agents/NF-1") fell outside the prefix
// and was refused by the capability check. The run now names a real branch
// inside its own grant.
func RunBranch(writeBranch string) string {
	if writeBranch == "" {
		return ""
	}
	if strings.HasSuffix(writeBranch, "/") {
		return writeBranch + runBranchLeaf
	}
	return writeBranch
}
