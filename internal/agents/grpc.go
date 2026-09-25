package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/events"
)

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

// defaultGrantTTL is the existing maximum grant lifetime. A run must leave
// room within it for the credential/evidence settlement allowance.
const defaultGrantTTL = 24 * time.Hour

// ExecuteFunc takes over a run once it has been created and issued its
// grant: provisioning its isolated workspace, running the agent loop, and
// driving it to a terminal state. It is invoked in a fresh background
// context (a running agent must outlive the StartRun request that
// launched it) after the run's state has already moved to "running". When
// nil, StartRun refuses admission before issuing any authority.
type ExecuteFunc func(ctx context.Context, run Run)

// GrantAuthority is implemented by the grant owner's API adapter in production.
// Agent-runtime never needs a transaction across the owner's schema and its own.
type RunGrantAuthority interface {
	IssueIntent(context.Context, capability.IssuanceIntent) (capability.Grant, error)
	CancelIssuance(context.Context, capability.IssuanceIntent) error
}

type GrantAuthority interface {
	Issue(context.Context, capability.Grant) (capability.Grant, error)
	Revoke(context.Context, uuid.UUID) error
}

// GRPCServer implements agentsv1.AgentServiceServer. Every method derives
// the caller's organization from authz.FromContext and passes it as an
// explicit predicate to every query — never from a field of the request
// message, so a client cannot simply name a different org and be believed.
type GRPCServer struct {
	agentsv1.UnimplementedAgentServiceServer

	Store   *Store
	Grants  GrantAuthority
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
	// HasModelPrices permits admission when a repository may select a priced
	// override even though the deployment default is unpriced. Runner still
	// requires the exact effective model price before any model call.
	HasModelPrices bool

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
func NewGRPCServer(store *Store, grants GrantAuthority, rdb *redis.Client, work workv1.WorkServiceClient, execute ExecuteFunc) *GRPCServer {
	if grants != nil {
		store.GrantRevoker = grants.Revoke
		if issuer, ok := grants.(RunGrantAuthority); ok {
			store.GrantIssuanceCanceller = issuer.CancelIssuance
		}
	}
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
		RepoId:                r.RepoID.String(), TokensAvailable: r.TokensAvailable, CostAvailable: r.CostAvailable,
		GrantCleanupPending: r.GrantCleanupPending, GrantCleanupError: r.GrantCleanupError,
		WorkReleasePending: r.WorkReleasePending, WorkReleaseError: r.WorkReleaseError,
		WorkspaceCleanupPending: r.WorkspaceCleanupPending, WorkspaceCleanupError: r.WorkspaceCleanupError,
		ExecutionFinished: r.ExecutionFinished,
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
	if g.Grants == nil || g.Store.GrantRevoker == nil {
		return nil, status.Error(codes.Unavailable, "grant authority and revoker are required to start a run")
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
	// The agent must be one of this organization's. The id used to be taken
	// as given, so a member of one organization could issue a capability
	// grant in their own organization to another organization's agent.
	if _, err := g.Store.GetAgent(ctx, agentID); err != nil {
		return nil, status.Errorf(codes.NotFound, "no agent %s in this organization", agentID)
	}
	// A person starting a run answers for it themself. Naming someone else as
	// the sponsor let a caller put any user id — another organization's
	// person included — on the record as accountable for their run. The
	// platform's own workers (the swarm scheduler, CI agent jobs) name the
	// sponsor they derived from the organization's own records.
	if scope, _ := authz.FromContext(ctx); scope.ActorKind == "service" {
		if scope.ServiceName != "swarm-scheduler" && scope.ServiceName != "ci-agent-jobs" {
			return nil, status.Error(codes.PermissionDenied, "service may not sponsor runs")
		}
	} else if scope.ActorKind != "user" || scope.ActorID != sponsorID {
		return nil, status.Error(codes.PermissionDenied, "a person may only sponsor a run they start themself")
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
	if req.GetCostLimitMicros() > 0 && g.Price == nil && !g.HasModelPrices {
		return nil, status.Error(codes.InvalidArgument, "this deployment prices no model tokens, so a cost limit could never be reached; "+
			"start the run without cost_limit_micros, or have the operator set the model's price in AI_MODEL_PRICES")
	}
	if itemResp.GetItem().GetAwaitingApproval() {
		return nil, status.Errorf(codes.FailedPrecondition, "work item %q is a maintenance proposal awaiting approval; approve it before starting an agent on it", req.GetWorkItemKey())
	}

	wallclock, err := RunWallclockLimit(req.GetWallclockLimitSeconds())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	issuer, issueOK := g.Grants.(RunGrantAuthority)
	if g.Execute == nil || !issueOK || g.Store.GrantRevoker == nil || g.Store.GrantIssuanceCanceller == nil || g.Store.WorkClaims == nil {
		return nil, status.Error(codes.FailedPrecondition, "executor, Work claim and stable grant authority must be available before admission")
	}
	grant := capability.Grant{
		ID:            uuid.New(),
		OrgID:         orgID,
		SubjectID:     agentID,
		SubjectKind:   "agent",
		RepoRead:      true,
		WriteBranch:   BranchNamespace + req.GetWorkItemKey() + "/",
		SecretsProd:   false,
		DeployStaging: false,
		DeployProd:    false,
		ExpiresAt:     time.Now().Add(wallclock + OrphanGrace),
	}

	intent := capability.IssuanceIntent{IssuerID: sponsorID, IssuerKind: "user", Grant: grant}

	run, err := g.Store.CreateRun(ctx, Run{
		OrgID:             orgID,
		RepoID:            repoID,
		AgentID:           agentID,
		WorkItemID:        workItemID,
		SponsorID:         sponsorID,
		GrantID:           grant.ID,
		GrantIntent:       &intent,
		WorkClaimRequired: true,
		Branch:            RunBranch(grant.WriteBranch),
		WallclockLimit:    wallclock,
		TokenLimit:        req.GetTokenLimit(),
		CostLimitMicros:   req.GetCostLimitMicros(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create run: %v", err)
	}

	// Every remote side effect now has a durable identity and compensation
	// obligation. Cancellation/timeout is ambiguous, never proof of no claim.
	admissionCtx, stopAdmission := context.WithTimeout(ctx, 30*time.Second)
	defer stopAdmission()
	admissionErr := func() error {
		var item *workv1.WorkItem
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			item, err = g.Store.WorkClaims.ClaimExecution(admissionCtx, run.ExecutionClaim())
			if err == nil {
				break
			}
		}
		if err != nil {
			return err
		}
		if err = g.Store.FreezeWorkItem(admissionCtx, run, item); err != nil {
			return err
		}
		issued, err := issuer.IssueIntent(admissionCtx, *run.GrantIntent)
		if err != nil {
			return err
		}
		if issued.ID != grant.ID || issued.OrgID != grant.OrgID || issued.SubjectID != grant.SubjectID || issued.WriteBranch != grant.WriteBranch || !issued.ExpiresAt.Equal(grant.ExpiresAt) || issued.RepoRead != grant.RepoRead || issued.SubjectKind != grant.SubjectKind || issued.SecretsProd || issued.DeployProd || issued.DeployStaging {
			return fmt.Errorf("grant owner returned mismatched issuance")
		}
		return nil
	}()
	if admissionErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = g.Store.SetRunState(cleanupCtx, run.ID, "failed")
		if err := g.Store.CleanupRunGrant(cleanupCtx, run.ID); err != nil {
			log.Printf("agents: admission cleanup pending for %s: %v", run.ID, err)
		}
		return nil, status.Error(codes.FailedPrecondition, "run admission incomplete; durable cleanup pending")
	}

	g.publishStateChange(run.ID, "", "queued")

	go g.driveToRunning(run)

	return &agentsv1.StartRunResponse{Run: toProtoRun(run)}, nil
}

// driveToRunning transitions a freshly created run from queued to running
// and publishes that change, then hands off to Execute if one is
// configured. It runs in the background, detached from the StartRun
// request's context, since an agent run outlives the RPC that started it.
//
// Moving to running is taking the branch lock (BranchLock.Acquire): the run
// holds its grant's prefix from this moment until it reaches any terminal
// state, and the git transports refuse everyone else a push there meanwhile.
// BranchLock existed, was tested, and nothing acquired it — every person
// could push to an agent's branch in the middle of its run.
func (g *GRPCServer) driveToRunning(run Run) {
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: run.OrgID, ActorID: run.AgentID, ActorKind: "agent"})
	if err := NewBranchLock(g.Store.Pool()).Acquire(ctx, run.OrgID, run.RepoID, run.Branch, run.ID); err != nil {
		// A run that cannot take its branch — another run on the same Work
		// Item still holds it — fails saying so. It used to return silently
		// and leave the run queued forever, which reads as a platform that
		// decided not to start it.
		current, getErr := g.Store.GetRun(ctx, run.ID)
		if getErr != nil || current.State != "queued" {
			return // cancelled before it could start; nothing to settle
		}
		if spendErr := g.Store.RecordSpend(ctx, run.ID, Spend{Reason: "could not start: " + err.Error()}); spendErr != nil {
			log.Printf("agents: record why run %s could not start: %v", run.ID, spendErr)
		}
		if stateErr := g.Store.SetRunState(ctx, run.ID, "failed"); stateErr != nil {
			log.Printf("agents: fail run %s that could not take its branch: %v", run.ID, stateErr)
			return
		}
		g.publishStateChange(run.ID, "queued", "failed")
		if err := g.Store.CleanupRunGrant(ctx, run.ID); err != nil {
			log.Printf("agents: grant cleanup pending for run %s: %v", run.ID, err)
		}
		return
	}
	g.publishStateChange(run.ID, "queued", "running")
	run.State = "running"

	if g.Execute != nil {
		current, err := g.Store.GetRun(ctx, run.ID)
		if err != nil {
			log.Printf("agents: read acquired run %s: %v", run.ID, err)
			return
		}
		run = current
		deadlineCtx, stopClock := context.WithDeadlineCause(ctx, run.StartedAt.Add(run.WallclockLimit), ErrOverBudget)
		defer stopClock()
		runCtx, cancel := g.Store.WithRunCancellation(deadlineCtx, run.ID)
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
	if run.State == "cancelled" {
		if err := g.Store.CleanupRunGrant(ctx, id); err != nil {
			return nil, status.Error(codes.Unavailable, "run cancelled; grant cleanup pending")
		}
		return &agentsv1.CancelRunResponse{Ok: true}, nil
	}
	if err := g.Store.SetRunState(ctx, id, "cancelled"); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cancel run: %v", err)
	}
	g.abort(id)
	g.publishStateChange(id, run.State, "cancelled")
	if err := g.Store.CleanupRunGrant(ctx, id); err != nil {
		return nil, status.Error(codes.Unavailable, "run cancelled; grant cleanup pending")
	}
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
	// The event stream is shared by every organization, so the run is looked
	// up in the caller's organization first; filtering the stream by run id
	// alone would hand anyone holding another organization's run id its tool
	// calls and states.
	if _, err := g.Store.GetRun(ctx, runID); err != nil {
		return status.Errorf(codes.NotFound, "no run %s in this organization", runID)
	}
	if g.RDB == nil {
		return status.Error(codes.Unavailable, "event stream is not configured")
	}

	lastID := "0"
	cursor := req.GetAfterCursor()
	audit := NewAuditLog(g.Store.pool)
	if _, err := audit.ToolEvents(ctx, runID, cursor); err != nil {
		if errors.Is(err, ErrToolEventCursor) {
			return status.Error(codes.InvalidArgument, "tool event cursor unavailable")
		}
		return status.Error(codes.Unavailable, "tool event history unavailable")
	}
	// grpc-go can return empty headers without an error for trailers-only
	// failures. Edge needs an explicit marker to distinguish a validated idle
	// stream from a terminal error before committing an HTTP SSE response.
	if err := stream.SendHeader(metadata.Pairs("x-novaforge-stream-ready", "1")); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		batch, err := audit.ToolEvents(ctx, runID, cursor)
		if err != nil {
			return status.Error(codes.Unavailable, "tool event history unavailable")
		}
		for _, e := range batch {
			if err = stream.Send(&agentsv1.StreamRunEventsResponse{RunId: runID.String(), At: e.At.Format(rfc3339), Cursor: e.Cursor, ToolCallId: e.CallID.String(), Payload: &agentsv1.StreamRunEventsResponse_ToolCall{ToolCall: &agentsv1.ToolCall{Tool: e.Tool, Outcome: e.Outcome}}}); err != nil {
				return err
			}
			cursor = e.Cursor
		}
		if len(batch) == 100 {
			continue
		}
		res, err := g.RDB.XRead(ctx, &redis.XReadArgs{
			Streams: []string{events.StreamAgentEvents, lastID},
			Block:   250 * time.Millisecond,
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
				if !ok || evt.RunID != runID || evt.Type == "tool_call" {
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
