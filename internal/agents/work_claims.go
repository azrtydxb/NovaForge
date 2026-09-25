package agents

import (
	"context"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// WorkServiceClaims is the production ExecutionClaims: it takes and releases a
// Work Item's execution claim through the Work service's RPC.
//
// Admission refuses to start a run without one, because the claim is what makes
// "this Work Item is being executed by exactly this run" true across replicas.
// Only a test double implemented this interface for a while, so every unit test
// admitted runs and the deployed agent-runtime refused all of them with
// "executor, Work claim and stable grant authority must be available before
// admission" — a seam both halves of which were written and tested.
type WorkServiceClaims struct{ Client workv1.WorkServiceClient }

func (w WorkServiceClaims) ClaimExecution(ctx context.Context, c ExecutionClaim) (*workv1.WorkItem, error) {
	res, err := w.Client.ClaimExecution(ctx, &workv1.ClaimExecutionRequest{
		WorkItemId: c.WorkItemID.String(),
		RepoId:     c.RepoID.String(),
		RunId:      c.RunID.String(),
		AgentId:    c.AgentID.String(),
		SponsorId:  c.SponsorID.String(),
	})
	if err != nil {
		return nil, err
	}
	return res.GetItem(), nil
}

func (w WorkServiceClaims) ReleaseExecution(ctx context.Context, r ExecutionRelease) error {
	_, err := w.Client.ReleaseExecution(ctx, &workv1.ReleaseExecutionRequest{
		WorkItemId:         r.WorkItemID.String(),
		RepoId:             r.RepoID.String(),
		RunId:              r.RunID.String(),
		Outcome:            r.Outcome,
		NoExecutionStarted: r.NoExecutionStarted,
	})
	return err
}
