package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/tools"
)

// genericToolSchema is used for every tool offered to the model: the
// registry's Handler takes raw JSON and validates its own shape, so no
// per-tool schema is required for the model-facing tool definition beyond
// "an object".
var genericToolSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)

// hardStepCap bounds the loop even when no budget dimension catches a
// misbehaving model or a bug in the stop condition — a safety net, not a
// budget dimension in its own right.
const hardStepCap = 200

// Result is the outcome of one Loop.Execute call.
type Result struct {
	State      string
	Steps      int
	TokensUsed int64
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
	messages := []provider.Message{
		provider.SystemText("You are an autonomous NovaForge engineering agent. Use the " +
			"provided tools to accomplish the assigned work item; call no tool that isn't offered. " +
			"Start by reading the work item to learn what is being asked of you. " +
			"You may write only to the branch named below."),
		provider.UserText(OpeningBrief(run)),
	}

	var steps int
	var tokensUsed int64

	for {
		if err := l.Budget.Check(); err != nil {
			return l.finish(ctx, run, "over_budget", steps, tokensUsed, "budget exceeded before model call")
		}
		if steps >= hardStepCap {
			return l.finish(ctx, run, "failed", steps, tokensUsed, "step cap exceeded without completion")
		}

		resp, err := l.Model.Generate(ctx, provider.Call{
			Messages:        messages,
			Tools:           toolDefs,
			ProviderOptions: l.ProviderOptions,
		})
		if err != nil {
			return l.finish(ctx, run, "failed", steps, tokensUsed, fmt.Sprintf("model call failed: %v", err))
		}
		steps++

		used := int64(resp.Usage.TotalTokens)
		tokensUsed += used
		l.Budget.AddTokens(used)

		calls := resp.ToolCalls()
		if len(calls) == 0 {
			return l.finish(ctx, run, "succeeded", steps, tokensUsed, resp.Text())
		}

		messages = append(messages, assistantMessage(resp, calls))

		for _, call := range calls {
			if err := l.Budget.Check(); err != nil {
				return l.finish(ctx, run, "over_budget", steps, tokensUsed, "budget exceeded before tool call")
			}

			result, callErr := reg.Call(ctx, run.ID, call.Name, call.Args)
			messages = append(messages, toolResultMessage(call, result, callErr))
		}
	}
}

// buildToolDefs offers every tool the registry knows about to the model,
// each with a permissive generic schema: argument validation happens
// inside the registry's handlers, not in the model-facing tool definition.
func buildToolDefs(reg *tools.Registry) []provider.ToolDef {
	names := reg.Names()
	defs := make([]provider.ToolDef, 0, len(names))
	for _, name := range names {
		defs = append(defs, provider.ToolDef{
			Name:        name,
			Description: name,
			Schema:      genericToolSchema,
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
func (l *Loop) finish(ctx context.Context, run agents.Run, state string, steps int, tokensUsed int64, summary string) (Result, error) {
	if l.Audit != nil {
		argsJSON, err := json.Marshal(map[string]string{"state": state, "summary": summary})
		if err == nil {
			id, recErr := l.Audit.Record(ctx, agents.Entry{RunID: run.ID, Tool: "run.summary", ArgsJSON: argsJSON})
			if recErr == nil {
				_ = l.Audit.Complete(ctx, id, "ok", "")
			}
		}
	}
	return Result{State: state, Steps: steps, TokensUsed: tokensUsed, Summary: summary}, nil
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
