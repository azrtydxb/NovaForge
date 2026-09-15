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

// MayMerge asks the gate controller whether runID may merge.
func (g GatesClient) MayMerge(ctx context.Context, runID uuid.UUID) (bool, []string, error) {
	resp, err := g.Gates.MayMerge(ctx, &gatesv1.MayMergeRequest{RunId: runID.String()})
	if err != nil {
		return false, nil, fmt.Errorf("gates: may merge %s: %w", runID, err)
	}
	return resp.GetAllowed(), resp.GetReasons(), nil
}
