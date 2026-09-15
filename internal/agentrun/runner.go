package agentrun

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/tools"
)

// Runner executes an agent run once its workspace exists: it prepares the
// run from the repository and the platform, builds the tool registry the
// repository allows, and drives the model/tool loop to a terminal state.
//
// It is what agent-runtime calls for every run. It lives here rather than in
// the service's main package so the path a production run takes — including
// how configuration governs it and what context reaches the model — is the
// path its tests take.
type Runner struct {
	Git   gitv1.GitServiceClient
	Work  workv1.WorkServiceClient
	Graph graphv1.GraphServiceClient

	Runs  *agents.Store
	Audit *agents.AuditLog

	// NewModel builds the model client for a model name; "" asks for the
	// deployment's configured model.
	NewModel func(model string) (provider.LanguageModel, error)
	// ProviderOptions carries deployment-configured wire parameters.
	ProviderOptions map[string]any
	// Price, when the deployment configures one, accrues the run's cost.
	Price *agents.TokenPrice
	// MCP, when set, lists the organization's approved external MCP servers,
	// whose tools the run is offered alongside the built-in ones.
	MCP tools.ApprovedMCPServers
}

// Run executes run with rt's clients and workspace, returning its outcome.
// Every exit is recorded as the run's summary, including a run that could
// not start because its repository's configuration does not parse: that
// failure is the configuration's author's to fix, and it is on the record
// where they will look.
func (r *Runner) Run(ctx context.Context, run agents.Run, rt tools.Runtime) Result {
	loop := NewLoop(nil, nil, r.Audit)
	loop.Runs = r.Runs
	fail := func(summary string) Result {
		log.Printf("agentrun: run %s: %s", run.ID, summary)
		res, _ := loop.finish(ctx, run, "failed", 0, spend{}, summary)
		return res
	}

	agent, err := r.Runs.GetAgent(ctx, run.AgentID)
	if err != nil {
		return fail(fmt.Sprintf("the run could not start: read its agent: %v", err))
	}
	plan, err := Prepare(ctx, Services{Git: r.Git, Work: r.Work, Graph: r.Graph}, run, agent)
	if err != nil {
		return fail(fmt.Sprintf("the run could not start: %v", err))
	}

	model, err := r.NewModel(plan.Model)
	if err != nil {
		return fail(fmt.Sprintf("the run could not start: build model client for %q: %v", plan.Model, err))
	}
	budget := agents.NewBudget(plan.WallclockLimit, plan.TokenLimit, plan.CostLimitMicros)

	rt.RunID = run.ID
	rt.Budget = budget
	reg := tools.NewRegistry(rt, r.Audit)
	// The organization's approved external MCP servers are offered before the
	// repository's tool list is applied, so an agent whose definition lists
	// its tools gets only the external tools it names. A register nobody read
	// made approval meaningless.
	if r.MCP != nil {
		offered, err := tools.OfferApprovedMCPServers(ctx, reg, r.MCP)
		if err != nil {
			log.Printf("agentrun: run %s: external MCP servers unavailable: %v", run.ID, err)
		} else if len(offered.Tools) > 0 {
			log.Printf("agentrun: run %s offered %d external MCP tool(s)", run.ID, len(offered.Tools))
		}
	}
	reg.Restrict(plan.Tools)

	loop.Model = model
	loop.Budget = budget
	loop.ProviderOptions = r.ProviderOptions
	loop.Price = r.Price
	loop.Brief = plan.Brief
	loop.Criteria = func(ctx context.Context, workItemID uuid.UUID) (Criteria, error) {
		resp, err := r.Work.GetItem(ctx, &workv1.GetItemRequest{Id: workItemID.String()})
		if err != nil {
			return Criteria{}, err
		}
		return Criteria{Goal: resp.GetItem().GetGoal(), Acceptance: resp.GetItem().GetAcceptance()}, nil
	}

	result, err := loop.Execute(ctx, run, reg)
	if err != nil {
		return fail(fmt.Sprintf("the run's loop failed: %v", err))
	}
	return result
}

// WithRunIdentity attaches the run's credential to every outbound call an
// agent run makes. An agent run outlives the request that started it, so it
// cannot borrow that caller's credential. The credential names one
// organization, so a run cannot reach outside it, and the run's agent, whose
// capability grant git-platform applies to every write.
func WithRunIdentity(ctx context.Context, hmacSecret string, orgID, agentID uuid.UUID, ttl time.Duration) (context.Context, error) {
	tok, err := svcauth.MintAgentRun(hmacSecret, orgID, agentID, ttl)
	if err != nil {
		return ctx, fmt.Errorf("mint run credential: %w", err)
	}
	return metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok,
		"x-novaforge-org", orgID.String(),
	), nil
}

// runCredentialMargin outlasts the run's own wall-clock stop, so the run can
// still settle and record its evidence after the limit trips.
const runCredentialMargin = 15 * time.Minute

// unboundedRunCredentialTTL bounds the credential of a run with no wall-clock
// limit; the orphan reaper settles such a run long before it lapses.
const unboundedRunCredentialTTL = 12 * time.Hour

// RunCredentialTTL is how long a run's credential lives. It is minted once per
// run, and was minted with the five-minute worker TTL: every service call a
// run made after its fifth minute was refused as unauthenticated.
func RunCredentialTTL(run agents.Run) time.Duration {
	if run.WallclockLimit > 0 {
		return run.WallclockLimit + runCredentialMargin
	}
	return unboundedRunCredentialTTL
}
