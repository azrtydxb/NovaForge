package reviews

import (
	"context"
	"fmt"

	"github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
)

// DefaultReviewRoles are the roles ReviewRun dispatches to when the caller
// names none: an independent reviewer, security, test, and architecture
// agent — matching the spec's "independent reviewer, security, test, and
// architecture agents review it" requirement, so a change is never both
// authored and solely reviewed by the same agent.
var DefaultReviewRoles = []string{"reviewer", "security", "test", "architecture"}

// AgentVerdict is one review agent's verdict on a run.
type AgentVerdict struct {
	Role      string
	AgentName string
	ModelName string
	Verdict   string // "approve", "request_changes", or "comment"
	Summary   string
}

// reviewOutput is the structured shape a review model call must return.
type reviewOutput struct {
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

// reviewCandidate pairs a role with the agent dispatched to review under
// it, once the author has been filtered out.
type reviewCandidate struct {
	role  string
	agent agents.Agent
}

// AgentReviewer dispatches independent multi-agent review over an
// Engineering Run: one model call per role, none of them the run's author
// agent, spread across distinct models where more than one is configured
// to avoid correlated failure between reviewer and author.
type AgentReviewer struct {
	Store *Store

	// RoleAgent maps a review role to the enabled agent that performs it.
	// A role with no entry is simply skipped — there is no agent to
	// dispatch to — rather than treated as an error, so ReviewRun
	// tolerates a partially configured reviewer set.
	RoleAgent map[string]agents.Agent

	// Models is the pool of models ReviewRun round-robins across roles for
	// model diversity. Exactly one configured model is allowed: every role
	// then shares it, Warn (if set) receives a note explaining why, and
	// review proceeds — a single available model must never block review
	// outright, since air-gapped deployments may only have one.
	Models []provider.LanguageModel
	// Warn receives a human-readable note when model diversity degrades
	// (e.g. to a single shared model). Nil is fine: nothing is logged.
	Warn func(msg string)

	// ProviderOptions carries deployment-configured wire parameters for the
	// model server (see agentrun.ParseProviderOptions) — the same options
	// the planner sends, for the same reason: a reviewer asked for a
	// structured verdict must produce one, not a monologue.
	ProviderOptions map[string]any
}

// ReviewRun dispatches independent review to every role in roles (or
// DefaultReviewRoles when roles is empty), each by a distinct agent, on a
// round-robined model. The run's author agent is filtered out of the
// candidate list BEFORE any model is called — filtering happens once,
// up front, never as a post-hoc check on what came back — so an agent can
// never review its own run under a different role name.
//
// Each verdict is recorded twice: as a gate-shaped proof entry through
// Store.RecordProof (so it appears in the run's PROOF block), and as a
// review through Store.SubmitReview (so an "approve" verdict participates
// in reviews.Merger's existing independent-approval check exactly like a
// human reviewer's would — this file adds no privileged path and does not
// touch merge.go). SubmitReview's own author-rejection is what makes a
// self-review structurally impossible even if RoleAgent were ever
// misconfigured to map a role onto the author's own agent.
//
// Review verdicts never gate a merge by themselves: reviews.Merger checks
// independent approval AND the gate controller's MayMerge, and MayMerge
// consults gate evaluations only — recording review verdicts here can
// never substitute for a required gate passing.
func (r *AgentReviewer) ReviewRun(ctx context.Context, runID uuid.UUID, roles []string) ([]AgentVerdict, error) {
	if len(roles) == 0 {
		roles = DefaultReviewRoles
	}

	run, err := r.Store.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("review run %s: %w", runID, err)
	}

	candidates := r.filterCandidates(roles, run)
	if len(candidates) == 0 {
		return nil, nil
	}

	if len(r.Models) == 0 {
		return nil, fmt.Errorf("review run %s: no models configured", runID)
	}
	if len(r.Models) == 1 && r.Warn != nil {
		r.Warn("agent review: only one model configured; every reviewer role will share it")
	}

	verdicts := make([]AgentVerdict, 0, len(candidates))
	for i, c := range candidates {
		model := r.Models[i%len(r.Models)]

		out, err := dispatchReview(ctx, model, run, c.role, r.ProviderOptions)
		if err != nil {
			return nil, fmt.Errorf("review run %s: dispatch role %q: %w", runID, c.role, err)
		}

		verdicts = append(verdicts, AgentVerdict{
			Role:      c.role,
			AgentName: c.agent.Name,
			ModelName: modelName(model),
			Verdict:   out.Verdict,
			Summary:   out.Summary,
		})

		if err := r.Store.RecordProof(ctx, runID, "review:"+c.role, proofStatus(out.Verdict), out.Summary); err != nil {
			return nil, fmt.Errorf("review run %s: record proof for role %q: %w", runID, c.role, err)
		}
		if err := r.Store.SubmitReview(ctx, runID, c.agent.ID, "agent", out.Verdict); err != nil {
			return nil, fmt.Errorf("review run %s: submit review for role %q: %w", runID, c.role, err)
		}
	}

	return verdicts, nil
}

// filterCandidates resolves roles against r.RoleAgent and drops the run's
// author agent, BEFORE any model call is made: this is the entire
// enforcement point for "an agent must not be both author and sole
// reviewer of a change" as far as ReviewRun's own dispatch is concerned.
func (r *AgentReviewer) filterCandidates(roles []string, run Run) []reviewCandidate {
	var candidates []reviewCandidate
	for _, role := range roles {
		agent, ok := r.RoleAgent[role]
		if !ok {
			continue
		}
		if run.AuthorKind == "agent" && agent.ID == run.AuthorID {
			continue
		}
		candidates = append(candidates, reviewCandidate{role: role, agent: agent})
	}
	return candidates
}

// proofStatus maps a review verdict onto the gate-shaped status
// RecordProof expects: an approval passes, a request for changes fails,
// and a bare comment is neither — recorded as passing so it never blocks
// merge by itself (only a gate, or the lack of independent approval, does
// that).
func proofStatus(verdict string) string {
	if verdict == "request_changes" {
		return "fail"
	}
	return "pass"
}

// modelName renders a model's identity as "<provider>/<model>" for
// AgentVerdict.ModelName.
func modelName(m provider.LanguageModel) string {
	if m == nil {
		return ""
	}
	return m.ProviderName() + "/" + m.ModelID()
}

// reviewSystemPromptTemplate instructs the model to act as one independent
// review role.
const reviewSystemPromptTemplate = "You are the %s reviewer in an independent, multi-agent code review. " +
	"You did not author this change. Review it strictly from the %s perspective and return a verdict of " +
	"\"approve\", \"request_changes\", or \"comment\", with a short summary explaining why."

// dispatchReview makes one structured-output model call for role against
// run, decoding the model's verdict and summary.
func dispatchReview(ctx context.Context, model provider.LanguageModel, run Run, role string, providerOptions map[string]any) (reviewOutput, error) {
	prompt := fmt.Sprintf("Run %q: %s -> %s, authored by %s.", run.Title, run.SourceRef, run.TargetRef, run.AgentName)
	result, err := ai.GenerateText(ctx, ai.GenerateTextOpts{
		Model:           model,
		System:          fmt.Sprintf(reviewSystemPromptTemplate, role, role),
		Prompt:          prompt,
		Output:          ai.OutputObject[reviewOutput](),
		ProviderOptions: providerOptions,
	})
	if err != nil {
		return reviewOutput{}, err
	}
	out, err := ai.OutputAs[reviewOutput](result)
	if err != nil {
		return reviewOutput{}, err
	}
	switch out.Verdict {
	case "approve", "request_changes", "comment":
	default:
		return reviewOutput{}, fmt.Errorf("model returned unknown verdict %q", out.Verdict)
	}
	return out, nil
}
