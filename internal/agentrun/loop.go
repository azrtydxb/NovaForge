package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/tools"
)

// hardStepCap bounds the loop even when no budget dimension catches a
// misbehaving model or a bug in the stop condition — a safety net, not a
// budget dimension in its own right.
const hardStepCap = 200

// Result is the outcome of one Loop.Execute call.
type Result struct {
	State      string
	Steps      int
	TokensUsed int64
	CostMicros int64
	Summary    string
}

// Loop drives one agent run's model/tool loop.
type Loop struct {
	Model  provider.LanguageModel
	Budget *agents.Budget

	// Audit, when non-nil, receives a final "run.summary" entry recording
	// the run's outcome — the loop's only persisted evidence beyond the
	// tool calls the registry itself audits. Model reasoning is never
	// written here or anywhere else.
	Audit *agents.AuditLog

	// ProviderOptions carries deployment-configured wire parameters for the
	// model server (see ParseProviderOptions), merged into every call this
	// loop makes.
	ProviderOptions map[string]any

	// Runs, when non-nil, receives the run's token spend when it finishes.
	Runs *agents.Store

	// Criteria, when non-nil, reads the run's Work Item so a run that stops
	// calling tools is verified against its acceptance criteria before it
	// may end "succeeded" (see verify.go). Production always sets it.
	Criteria CriteriaSource

	// Brief is the opening user message: the run's ids and everything
	// Prepare assembled for it. Empty, the loop opens with the ids alone.
	Brief string

	// Price is the token price of the model this loop calls, when the
	// deployment configures one. Without it no cost accrues, and StartRun has
	// already refused any run that asked for a cost limit.
	Price *agents.TokenPrice
}

// NewLoop builds a Loop bound to model and budget, persisting its final
// summary (and, transitively, every tool call reg.Call makes) through
// audit.
func NewLoop(model provider.LanguageModel, budget *agents.Budget, audit *agents.AuditLog) *Loop {
	return &Loop{Model: model, Budget: budget, Audit: audit}
}

// Execute assembles a request from run, calls the model through go-ai-sdk,
// dispatches any requested tool through reg, and repeats until the model
// returns no tool call or the budget check fails. Only observable actions
// are persisted: tool calls through reg's audit log, and a final summary
// string through l.Audit. Model reasoning is never written to any table.
func (l *Loop) Execute(ctx context.Context, run agents.Run, reg *tools.Registry) (Result, error) {
	toolDefs := buildToolDefs(reg)

	// The agent is told what it is working on, not just that it is working.
	// "Begin work on run <uuid>" was all it used to get: no work item, no
	// repository, no branch — so it could not call work.get (which needs the
	// work item's id) or any repo.* tool (which needs the repository), and
	// had nothing to do but guess.
	brief := l.Brief
	if brief == "" {
		brief = OpeningBrief(run)
	}
	messages := []provider.Message{
		provider.SystemText("You are an autonomous NovaForge engineering agent. Use the " +
			"provided tools to accomplish the assigned work item; call no tool that isn't offered. " +
			"The brief below carries the work item, the repository's own configuration and context, " +
			"project knowledge recorded by earlier work, and code assembled for this work item. " +
			"You may write only to the branch named below. When you stop calling tools, the run is " +
			"verified against the work item's acceptance criteria using only the tool calls you made: " +
			"anything you claim but did not do through a tool counts as not done."),
		provider.UserText(brief),
	}

	// The wall-clock limit is a deadline on every model and tool call, not
	// only a comparison between steps: a model call that never returned used
	// to hold the run past its limit for as long as the call took, which for a
	// hung gateway is forever.
	ctx, stopClock := context.WithDeadline(ctx, l.Budget.Deadline())
	defer stopClock()

	var steps int
	var spent spend
	var evidence []evidenceStep

	for {
		if l.cancelled(ctx, run) {
			return l.finish(ctx, run, "cancelled", steps, spent, "run cancelled before model call")
		}
		if err := l.overBudget(ctx); err != nil {
			return l.finish(ctx, run, "over_budget", steps, spent, "budget exceeded before model call: "+err.Error())
		}
		if steps >= hardStepCap {
			return l.finish(ctx, run, "failed", steps, spent, "step cap exceeded without completion")
		}

		resp, err := l.Model.Generate(ctx, provider.Call{
			Messages:        messages,
			Tools:           toolDefs,
			ProviderOptions: l.ProviderOptions,
		})
		if err != nil {
			// CancelRun aborts an in-flight call by cancelling ctx; that is a
			// cancellation, not a model failure, and must be recorded as one.
			if l.cancelled(ctx, run) {
				return l.finish(ctx, run, "cancelled", steps, spent, "run cancelled during model call")
			}
			// Likewise a call cut off by the wall-clock deadline was stopped
			// by the budget, not failed by the model.
			if budgetErr := l.overBudget(ctx); budgetErr != nil {
				return l.finish(ctx, run, "over_budget", steps, spent, "budget exceeded during model call: "+budgetErr.Error())
			}
			return l.finish(ctx, run, "failed", steps, spent, fmt.Sprintf("model call failed: %v", err))
		}
		steps++
		spent = l.account(spent, resp.Usage)

		calls := resp.ToolCalls()
		if len(calls) == 0 {
			// A run cancelled while its final answer was being generated was
			// still cancelled; reporting it succeeded would contradict the row.
			if l.cancelled(ctx, run) {
				return l.finish(ctx, run, "cancelled", steps, spent, "run cancelled during model call")
			}
			if l.Criteria == nil {
				return l.finish(ctx, run, "succeeded", steps, spent, resp.Text())
			}
			state, summary, verifyUsage := l.verify(ctx, run, evidence, resp.Text())
			spent = l.account(spent, verifyUsage)
			if budgetErr := l.overBudget(ctx); budgetErr != nil && state != "succeeded" {
				return l.finish(ctx, run, "over_budget", steps, spent, "budget exceeded during verification: "+budgetErr.Error())
			}
			return l.finish(ctx, run, state, steps, spent, summary)
		}

		messages = append(messages, assistantMessage(resp, calls))

		for _, call := range calls {
			if l.cancelled(ctx, run) {
				return l.finish(ctx, run, "cancelled", steps, spent, "run cancelled before tool call")
			}
			if err := l.overBudget(ctx); err != nil {
				return l.finish(ctx, run, "over_budget", steps, spent, "budget exceeded before tool call: "+err.Error())
			}

			result, callErr := reg.Call(ctx, run.ID, call.Name, call.Args)
			messages = append(messages, toolResultMessage(call, result, callErr))
			step := evidenceStep{Tool: call.Name, Args: string(call.Args), Result: string(result)}
			if callErr != nil {
				step.Err = callErr.Error()
			}
			evidence = append(evidence, step)
		}
	}
}

// spend is what the loop has consumed so far.
type spend struct {
	tokens     int64
	costMicros int64
}

// account adds one model response's usage to the run's spend and its budget.
// Cost accrues only when the deployment prices the model's tokens; a run in
// an unpriced deployment has no cost limit to reach (StartRun refuses one).
func (l *Loop) account(s spend, usage provider.Usage) spend {
	tokens := int64(usage.TotalTokens)
	s.tokens += tokens
	l.Budget.AddTokens(tokens)
	if l.Price != nil {
		cost := l.Price.CostMicros(usage.InputTokens, usage.OutputTokens)
		s.costMicros += cost
		l.Budget.AddCostMicros(cost)
	}
	return s
}

// overBudget reports which budget limit the run has exceeded, if any. The
// deadline check stands beside Budget.Check so a call cut off at the deadline
// is attributed to the wall clock even if Check's own clock reading is a
// hair short of it.
func (l *Loop) overBudget(ctx context.Context) error {
	if err := l.Budget.Check(); err != nil {
		return err
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: wallclock limit exceeded (deadline %s passed)", agents.ErrOverBudget, l.Budget.Deadline().UTC().Format(time.RFC3339))
	}
	return nil
}

// cancelled reports whether run has been cancelled, checked at every step
// boundary. CancelRun used to write "cancelled" to the row and nothing read
// it: the loop carried on calling tools — committing to the run's branch —
// until the model stopped on its own. The row is the signal because
// agent-runtime runs more than one replica and CancelRun may land on one
// that is not executing this loop; a cancelled ctx is the faster local
// signal for the replica that is.
func (l *Loop) cancelled(ctx context.Context, run agents.Run) bool {
	if l.Runs != nil {
		current, err := l.Runs.GetRun(context.WithoutCancel(ctx), run.ID)
		if err == nil {
			return current.State == "cancelled"
		}
		// A run whose row is gone was purged with its repository or its
		// organization. Treating that as a transient failure would let the
		// loop go on calling tools for something that no longer exists.
		if errors.Is(err, pgx.ErrNoRows) {
			return true
		}
		// A transient read failure must not end a healthy run; the context
		// is still consulted and the next step boundary checks again.
		log.Printf("agentrun: check cancellation of run %s: %v", run.ID, err)
	}
	// A deadline is the wall-clock limit, not a cancellation (see overBudget).
	return errors.Is(ctx.Err(), context.Canceled)
}

// buildToolDefs offers every tool the registry knows about to the model,
// each with a permissive generic schema: argument validation happens
// inside the registry's handlers, not in the model-facing tool definition.
func buildToolDefs(reg *tools.Registry) []provider.ToolDef {
	names := reg.Names()
	defs := make([]provider.ToolDef, 0, len(names))
	for _, name := range names {
		spec := reg.Spec(name)
		defs = append(defs, provider.ToolDef{
			Name:        name,
			Description: spec.Description,
			Schema:      spec.Schema,
		})
	}
	return defs
}

// assistantMessage carries forward the model's tool call requests (and any
// accompanying text) so the conversation can be resumed with their
// results. Reasoning content, if the provider returned any, is
// deliberately excluded: it lives only in resp for this in-memory step and
// is never written to a message that could later be persisted.
func assistantMessage(resp *provider.Response, calls []provider.ToolCallPart) provider.Message {
	content := make([]provider.ContentPart, 0, len(calls)+1)
	if text := resp.Text(); text != "" {
		content = append(content, provider.TextPart{Text: text})
	}
	for _, c := range calls {
		content = append(content, c)
	}
	return provider.Message{Role: provider.RoleAssistant, Content: content}
}

// toolResultMessage feeds a tool's outcome back to the model as a tool
// result rather than aborting the run: a tool error is surfaced to the
// model exactly like a successful result, just marked IsError.
func toolResultMessage(call provider.ToolCallPart, result []byte, callErr error) provider.Message {
	part := provider.ToolResultPart{ToolCallID: call.ID, Name: call.Name}
	if callErr != nil {
		part.IsError = true
		part.Result = callErr.Error()
	} else {
		part.Result = json.RawMessage(result)
	}
	return provider.Message{Role: provider.RoleTool, Content: []provider.ContentPart{part}}
}

// finish records state as the run's final evidence and returns the
// matching Result. It is the loop's only write path: it never has access
// to model reasoning, only the state string and summary text the caller
// (or the model's own final text, on natural completion) supplied.
func (l *Loop) finish(ctx context.Context, run agents.Run, state string, steps int, spent spend, summary string) (Result, error) {
	// A cancelled run's context is already done, and its evidence must still
	// be written — otherwise the spend and summary of exactly the runs someone
	// chose to stop are the ones lost.
	ctx = context.WithoutCancel(ctx)
	// What the run spent is recorded before anything else: the budget was
	// enforced in memory, and without this the number is gone the moment the
	// process moves on.
	if l.Runs != nil {
		if err := l.Runs.RecordSpend(ctx, run.ID, agents.Spend{Tokens: spent.tokens, CostMicros: spent.costMicros, Reason: endReason(state, summary)}); err != nil {
			log.Printf("agentrun: record spend for run %s: %v", run.ID, err)
		}
	}
	if l.Audit != nil {
		argsJSON, err := json.Marshal(map[string]string{"state": state, "summary": summary})
		if err == nil {
			id, recErr := l.Audit.Record(ctx, agents.Entry{RunID: run.ID, Tool: "run.summary", ArgsJSON: argsJSON})
			if recErr == nil {
				_ = l.Audit.Complete(ctx, id, "ok", "")
			}
		}
	}
	return Result{State: state, Steps: steps, TokensUsed: spent.tokens, CostMicros: spent.costMicros, Summary: summary}, nil
}

// maxEndReason bounds the reason stored on the run row; the full summary is
// in the run's audit log.
const maxEndReason = 1000

// endReason is what the run row records about why it ended. A run that
// succeeded needs no reason — its summary is the model's closing text, which
// is a claim, and belongs only in the audit log beside the evidence.
func endReason(state, summary string) string {
	if state == "succeeded" {
		return ""
	}
	if len(summary) > maxEndReason {
		return summary[:maxEndReason] + "…"
	}
	return summary
}

// OpeningBrief renders what an agent needs to begin: which work item, which
// repository, and the one branch its capability grant lets it write to.
func OpeningBrief(run agents.Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s.\n", run.ID)
	if run.WorkItemID != uuid.Nil {
		fmt.Fprintf(&b, "Work item: %s — read it with work.get before doing anything else.\n", run.WorkItemID)
	}
	if run.RepoID != uuid.Nil {
		fmt.Fprintf(&b, "Repository: %s — pass this as the \"repo\" argument to repo.* and git.* tools.\n", run.RepoID)
	}
	if run.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s — the only branch you may write to.\n", run.Branch)
	}
	return b.String()
}
