package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
)

// Criteria is what a run is judged against: the Work Item's goal and its
// acceptance criteria.
type Criteria struct {
	Goal       string
	Acceptance []string
}

// CriteriaSource reads the criteria of the Work Item a run was started for.
type CriteriaSource func(ctx context.Context, workItemID uuid.UUID) (Criteria, error)

// Verdict is the verifier's judgement of one acceptance criterion.
type Verdict struct {
	Criterion string `json:"criterion"`
	Met       bool   `json:"met"`
	Evidence  string `json:"evidence"`
}

type verification struct {
	Verdicts []Verdict `json:"verdicts"`
}

// evidenceStep is one observable action the run took: a tool call and what
// came back. The verifier sees these, never the agent's reasoning.
type evidenceStep struct {
	Tool   string
	Args   string
	Result string
	Err    string
}

// maxEvidenceField bounds each argument and result shown to the verifier, and
// maxEvidenceSteps the number of steps: a run that read a large file fifty
// times must not push the criteria out of the verifier's context.
const (
	maxEvidenceField = 2000
	maxEvidenceSteps = 60
)

const verifySystemPrompt = `You verify whether an autonomous engineering agent's run met its Work Item's acceptance criteria.
Judge ONLY from the evidence transcript: the tool calls the run made and what each returned.
The agent's closing message is a claim, not evidence. A criterion is met only when the transcript shows an action that satisfies it
(for example a successful git.commit containing the change, or a work.comment stating the conclusion).
A tool call that returned an error achieved nothing. When the evidence is absent or ambiguous, the criterion is not met.
Return exactly one verdict per criterion, in the order given, quoting the criterion verbatim; "evidence" names the specific
step(s) relied on, or says what is missing.`

// verify judges the run's evidence against its Work Item's acceptance
// criteria, records the verdicts as the run's "run.verification" entry, and
// returns the state the run ends in.
//
// A run used to be "succeeded" the moment the model stopped calling tools:
// an agent that read the work item and replied "done" succeeded exactly like
// one that did the work. The criteria were never consulted — work.get did not
// even return them to the agent.
func (l *Loop) verify(ctx context.Context, run agents.Run, steps []evidenceStep, claim string) (state, summary string, usage *provider.Usage) {
	crit, err := l.Criteria(ctx, run.WorkItemID)
	if err != nil {
		return "failed", fmt.Sprintf("could not read the work item's acceptance criteria to verify the run: %v", err), usage
	}
	if len(crit.Acceptance) == 0 {
		// Nothing to judge against is recorded as such rather than invented:
		// the run succeeds on the agent's own account, and the record says
		// that is all it rests on.
		if err := l.recordVerification(ctx, run, verification{}, "the work item declares no acceptance criteria; the run was not verified"); err != nil {
			return "failed", "verification evidence unavailable", usage
		}
		return "succeeded", claim, usage
	}

	usage = &provider.Usage{}
	result, err := ai.GenerateText(ctx, ai.GenerateTextOpts{
		Model:           l.Model,
		System:          verifySystemPrompt,
		Prompt:          verifyPrompt(crit, steps, claim),
		Output:          ai.OutputObject[verification](),
		ProviderOptions: l.ProviderOptions,
		OnModelCallEnd: func(end ai.ModelCallEnd) {
			if end.Response != nil {
				*usage = end.Response.Usage
			}
		},
	})
	if err != nil {
		return "failed", fmt.Sprintf("verification against the acceptance criteria failed: %v", err), usage
	}
	*usage = result.Usage
	if err := modelUsage(*usage, l.Price != nil); err != nil {
		return "failed", err.Error(), usage
	}
	v, err := ai.OutputAs[verification](result)
	if err != nil {
		return "failed", fmt.Sprintf("verification returned no decodable verdict: %v", err), usage
	}
	if len(v.Verdicts) != len(crit.Acceptance) {
		return "failed", fmt.Sprintf("verification returned %d verdicts for %d acceptance criteria", len(v.Verdicts), len(crit.Acceptance)), usage
	}
	// The criterion text is taken from the Work Item, not from the model's
	// echo of it, so the record cannot drift from what was actually asked.
	var unmet []string
	for i := range v.Verdicts {
		v.Verdicts[i].Criterion = crit.Acceptance[i]
		if !v.Verdicts[i].Met {
			unmet = append(unmet, fmt.Sprintf("%q (%s)", crit.Acceptance[i], v.Verdicts[i].Evidence))
		}
	}
	if err := l.recordVerification(ctx, run, v, ""); err != nil {
		return "failed", "verification evidence unavailable", usage
	}
	if len(unmet) > 0 {
		return "failed", "acceptance criteria not met: " + strings.Join(unmet, "; "), usage
	}
	return "succeeded", claim, usage
}

func (l *Loop) recordVerification(ctx context.Context, run agents.Run, v verification, note string) error {
	if l.Audit == nil {
		return nil
	}
	body := map[string]any{"verdicts": v.Verdicts}
	if v.Verdicts == nil {
		body["verdicts"] = []Verdict{}
	}
	if note != "" {
		body["note"] = note
	}
	argsJSON, err := json.Marshal(body)
	if err != nil {
		return err
	}
	id, err := l.Audit.Record(ctx, agents.Entry{RunID: run.ID, Tool: "run.verification", ArgsJSON: argsJSON})
	if err == nil {
		err = l.Audit.Complete(ctx, id, "ok", "")
	}
	return err
}

func verifyPrompt(crit Criteria, steps []evidenceStep, claim string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Work item goal:\n%s\n\nAcceptance criteria:\n", crit.Goal)
	for i, c := range crit.Acceptance {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c)
	}
	b.WriteString("\nEvidence transcript (tool calls in order):\n")
	if len(steps) == 0 {
		b.WriteString("(the run made no tool calls)\n")
	}
	start := 0
	if len(steps) > maxEvidenceSteps {
		start = len(steps) - maxEvidenceSteps
		fmt.Fprintf(&b, "(%d earlier steps omitted)\n", start)
	}
	for i, s := range steps[start:] {
		fmt.Fprintf(&b, "\n[step %d] %s\nargs: %s\n", start+i+1, s.Tool, clip(s.Args))
		if s.Err != "" {
			fmt.Fprintf(&b, "ERROR: %s\n", clip(s.Err))
		} else {
			fmt.Fprintf(&b, "result: %s\n", clip(s.Result))
		}
	}
	fmt.Fprintf(&b, "\nThe agent's closing claim (not evidence):\n%s\n", clip(claim))
	return b.String()
}

func clip(s string) string {
	if len(s) <= maxEvidenceField {
		return s
	}
	return s[:maxEvidenceField] + fmt.Sprintf("… (%d bytes omitted)", len(s)-maxEvidenceField)
}
