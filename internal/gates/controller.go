package gates

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// RunHead is what the controller needs to know about a run to resolve and
// evaluate its gates: its org/repo, the ref it targets, its current head
// SHA, and its Work Item's required gates.
type RunHead struct {
	OrgID         uuid.UUID
	RepoID        uuid.UUID
	TargetRef     string
	HeadSHA       string
	WorkItemGates []string
}

// RunLookup resolves the current RunHead for runID. In production this is
// backed by gRPC calls to the reviews and work services — the gates schema
// never reads their tables directly, per the one-schema-per-service rule.
// Tests provide an in-memory stub.
type RunLookup func(ctx context.Context, runID uuid.UUID) (RunHead, error)

// InputBuilder builds the Input a gate runner needs to evaluate one named
// gate for one run: its checked-out workspace, target/source SHAs, and the
// tool runner to invoke inside it. Tests provide their own; production
// checks out the run's SHAs into a workspace first.
type InputBuilder func(ctx context.Context, runID uuid.UUID, head RunHead, gate string, params map[string]any) (Input, error)

// Controller is the sole authority on merge eligibility. reviews.Merger asks
// it MayMerge and has no other path to merge: agents cannot bypass gates
// because there is structurally no other route to a merge decision.
type Controller struct {
	Store      *Store
	Git        gitv1.GitServiceClient
	Runs       RunLookup
	BuildInput InputBuilder
	// Proof, when set, records each gate's outcome as the run's proof in the
	// reviews service, which is what a run presents as its evidence. Without
	// it evaluations were stored here and shown nowhere.
	Proof ProofRecorder
}

// ProofRecorder records one gate's outcome against a run.
type ProofRecorder func(ctx context.Context, runID uuid.UUID, gate, status, detail string) error

// resolveForRun looks up runID's current head and resolves the gate
// definitions and latest evaluations that apply to it.
func (c *Controller) resolveForRun(ctx context.Context, runID uuid.UUID) (RunHead, []Definition, map[string]Evaluation, error) {
	head, err := c.Runs(ctx, runID)
	if err != nil {
		return RunHead{}, nil, nil, fmt.Errorf("resolve run head for %s: %w", runID, err)
	}
	defs, err := Resolve(ctx, c.Git, head.OrgID, head.RepoID, head.TargetRef, head.WorkItemGates)
	if err != nil {
		return RunHead{}, nil, nil, fmt.Errorf("resolve gate definitions: %w", err)
	}
	latest, err := c.Store.LatestForSHA(ctx, runID, head.HeadSHA)
	if err != nil {
		return RunHead{}, nil, nil, fmt.Errorf("load latest evaluations for %s: %w", runID, err)
	}
	return head, defs, latest, nil
}

// Evaluate runs every gate resolved for runID whose latest evaluation does
// not already match the run's current head SHA, recording each new result.
// A gate already evaluated at the current head is returned as-is rather
// than re-run, which is what makes re-evaluation idempotent under Redis's
// at-least-once redelivery.
func (c *Controller) Evaluate(ctx context.Context, runID uuid.UUID) ([]Evaluation, error) {
	head, defs, latest, err := c.resolveForRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	results := make([]Evaluation, 0, len(defs))
	for _, def := range defs {
		if eval, ok := latest[def.Name]; ok {
			if err := c.recordProof(ctx, runID, eval); err != nil {
				return nil, err
			}
			results = append(results, eval)
			continue
		}
		runner, ok := Runners[def.Name]
		if !ok {
			return nil, fmt.Errorf("no runner registered for gate %q", def.Name)
		}
		in, err := c.BuildInput(ctx, runID, head, def.Name, def.Params)
		if err != nil {
			return nil, fmt.Errorf("build input for gate %q: %w", def.Name, err)
		}
		eval, err := runner(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("run gate %q: %w", def.Name, err)
		}
		eval.OrgID = head.OrgID
		eval.RunID = runID
		eval.Gate = def.Name
		eval.TargetSHA = head.HeadSHA
		if err := c.Store.RecordEvaluation(ctx, eval); err != nil {
			return nil, fmt.Errorf("record evaluation for gate %q: %w", def.Name, err)
		}
		if err := c.recordProof(ctx, runID, eval); err != nil {
			return nil, err
		}
		results = append(results, eval)
	}
	return results, nil
}

// recordProof publishes one outcome as the run's proof. It is repeated for an
// outcome already cached at this head, so a proof write that failed once is
// made on the next evaluation rather than lost with the cache hit.
func (c *Controller) recordProof(ctx context.Context, runID uuid.UUID, eval Evaluation) error {
	if c.Proof == nil {
		return nil
	}
	if err := c.Proof(ctx, runID, eval.Gate, eval.Status, eval.Detail); err != nil {
		return fmt.Errorf("record proof for gate %q: %w", eval.Gate, err)
	}
	return nil
}

// MayMerge is a deny-by-default fold: the run may merge only when every
// required gate resolved from the target ref (unioned with the Work Item's
// required gates) has an evaluation for the run's current head SHA with
// status exactly "pass". Missing, stale (recorded for a different SHA),
// "fail", "error", and "skipped" all mean not-allowed and each appends a
// human-readable reason; an unrun gate is never treated as a pass.
func (c *Controller) MayMerge(ctx context.Context, runID uuid.UUID) (bool, []string, error) {
	head, defs, latest, err := c.resolveForRun(ctx, runID)
	if err != nil {
		return false, nil, err
	}

	var reasons []string
	for _, def := range defs {
		if !def.Required {
			continue
		}
		eval, ok := latest[def.Name]
		if !ok {
			reasons = append(reasons, fmt.Sprintf("gate %q not evaluated at head %s", def.Name, head.HeadSHA))
			continue
		}
		if eval.Status != "pass" {
			reasons = append(reasons, fmt.Sprintf("gate %q status %q", def.Name, eval.Status))
		}
	}
	return len(reasons) == 0, reasons, nil
}
