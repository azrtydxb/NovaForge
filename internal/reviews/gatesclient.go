package reviews

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
)

// GatesClient adapts the gates service's gRPC client to GateChecker and
// GateEvaluator. Merger asks it before any git operation, so auto-merge and a
// person merging pass through the identical check. It lived in
// cmd/work-reviews, so no test could join a merge to the real gate service
// through the adapter the deployment uses.
type GatesClient struct {
	Gates gatesv1.GatesServiceClient
}

// Evaluate runs the run's gates at its current head.
func (g GatesClient) Evaluate(ctx context.Context, runID uuid.UUID) error {
	if _, err := g.Gates.Evaluate(ctx, &gatesv1.EvaluateRequest{RunId: runID.String()}); err != nil {
		return fmt.Errorf("gates: evaluate %s: %w", runID, err)
	}
	return nil
}

// MayMerge asks the gate controller whether the run may merge.
func (g GatesClient) MayMerge(ctx context.Context, runID uuid.UUID) (bool, []string, error) {
	resp, err := g.Gates.MayMerge(ctx, &gatesv1.MayMergeRequest{RunId: runID.String()})
	if err != nil {
		return false, nil, fmt.Errorf("gates: may merge %s: %w", runID, err)
	}
	return resp.GetAllowed(), resp.GetReasons(), nil
}
