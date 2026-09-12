package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/work"
)

// gateClient adapts the gates service's gRPC client to reviews.GateChecker.
// Merger asks it before any git operation, so auto-merge and a person
// clicking merge pass through the identical check.
type gateClient struct {
	gates gatesv1.GatesServiceClient
}

func (g gateClient) MayMerge(ctx context.Context, runID uuid.UUID) (bool, []string, error) {
	resp, err := g.gates.MayMerge(ctx, &gatesv1.MayMergeRequest{RunId: runID.String()})
	if err != nil {
		return false, nil, fmt.Errorf("gates: may merge %s: %w", runID, err)
	}
	return resp.GetAllowed(), resp.GetReasons(), nil
}

// newAutoMerger builds the auto-merger from the policy the chart configures.
// It is nil when policy is disabled, so a deployment that has not opted in
// carries no auto-merge machinery at all rather than a disabled copy of it.
func newAutoMerger(policy reviews.AutoMergePolicy, store *reviews.Store, git gitv1.GitServiceClient, gates gatesv1.GatesServiceClient, workStore *work.Store) *reviews.AutoMerger {
	if !policy.Enabled {
		return nil
	}
	return &reviews.AutoMerger{
		Store:  store,
		Merger: &reviews.Merger{Store: store, Gates: gateClient{gates: gates}, Git: git},
		Policy: policy,
		Impact: func(ctx context.Context, run reviews.Run) (reviews.Impact, error) {
			return reviews.ComputeImpact(ctx, git, run.OrgID, run.RepoID, run.TargetRef, run.SourceRef)
		},
		WorkItemType: func(ctx context.Context, run reviews.Run) (string, error) {
			if run.WorkItemID == uuid.Nil {
				return "", fmt.Errorf("run %s carries no work item, so its type cannot be checked against policy", run.ID)
			}
			// The work schema is this service's own, so this is a local read
			// rather than a call back into itself over gRPC.
			item, err := workStore.Get(ctx, run.WorkItemID)
			if err != nil {
				return "", fmt.Errorf("resolve work item for run %s: %w", run.ID, err)
			}
			return item.Type, nil
		},
	}
}
