package agentrun

import (
	"context"
	"fmt"
	"log"

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

// WithRunIdentity attaches a service token for orgID to every outbound call
// an agent run makes. An agent run outlives the request that started it, so
// it cannot borrow that caller's credential; the token names one
// organization, so a run cannot reach outside the organization it belongs to
// even if a tool were asked to.
func WithRunIdentity(ctx context.Context, hmacSecret string, orgID uuid.UUID) (context.Context, error) {
	tok, err := svcauth.Mint(hmacSecret, svcauth.AgentRunService, orgID, svcauth.DefaultTTL)
	if err != nil {
		return ctx, fmt.Errorf("mint service token: %w", err)
	}
	return metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok,
		"x-novaforge-org", orgID.String(),
	), nil
}
