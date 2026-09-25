package reviews

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
)

// GatesClient adapts the gates service's gRPC client to GateChecker and
// GateEvaluator. Merger asks it before any git operation, so auto-merge and a
// person clicking merge pass through the identical check. It lives in this
// package rather than in cmd/work-reviews so that the tests joining a real
// merge to a real gate controller use the adapter production uses.
type GatesClient struct {
	Gates gatesv1.GatesServiceClient
}

// Evaluate runs the run's gates at its current head (see GateEvaluator).
func (g GatesClient) Evaluate(ctx context.Context, runID uuid.UUID) error {
	if _, err := g.Gates.Evaluate(ctx, &gatesv1.EvaluateRequest{RunId: runID.String()}); err != nil {
		return fmt.Errorf("gates: evaluate %s: %w", runID, err)
	}
	return nil
}

// MayMergePinned refuses unbound or mismatched authority even if allowed is true.
func (g GatesClient) MayMergePinned(ctx context.Context, runID uuid.UUID, sourceSHA, targetSHA string) (bool, []string, error) {
	if sourceSHA == "" || targetSHA == "" {
		return false, nil, fmt.Errorf("pinned source and target required")
	}
	resp, err := g.Gates.MayMerge(ctx, &gatesv1.MayMergeRequest{RunId: runID.String(), ExpectedSourceSha: sourceSHA, ExpectedTargetSha: targetSHA})
	if err != nil {
		return false, nil, fmt.Errorf("gates: may merge %s: %w", runID, err)
	}
	if resp.GetEvaluatedSourceSha() != sourceSHA || resp.GetEvaluatedTargetSha() != targetSHA {
		return false, nil, fmt.Errorf("gate response revision identity missing or mismatched")
	}
	return resp.GetAllowed(), resp.GetReasons(), nil
}
