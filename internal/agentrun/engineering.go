package agentrun

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
)

// EngineeringRunSpec is what a succeeded Agent Run knows about the change it
// made: where, for what, and who and what made it.
type EngineeringRunSpec struct {
	RepoID      uuid.UUID
	WorkItemID  uuid.UUID
	WorkItemKey string
	Goal        string
	// Acceptance becomes the run's plan: the steps the agent was held to, each
	// verified met before the run could succeed.
	Acceptance []string
	Branch     string
	AgentID    uuid.UUID
	AgentName  string
	// ModelName is the model the run actually executed on, not the one the
	// agent's definition names: the deployment decides which model serves.
	ModelName string
}

// titleLimit bounds an Engineering Run's title; a goal can be a paragraph.
const titleLimit = 120

// OpenEngineeringRun opens the Engineering Run through which a succeeded
// Agent Run's change is reviewed and merged, authored by the agent and naming
// the model that produced it, with the Work Item's acceptance criteria as its
// plan. It returns nil, nil when the branch holds no change — there is nothing
// to review — and returns the existing open run for the branch rather than a
// second one.
//
// Before this an agent's work reached a branch and stopped there: no run was
// opened for it, so it had no plan, no change impact, no proof and no record
// of which agent and model made it, and nothing could merge it but a person
// who went looking.
func OpenEngineeringRun(ctx context.Context, reviews reviewsv1.ReviewsServiceClient, git gitv1.GitServiceClient, spec EngineeringRunSpec) (*reviewsv1.Run, error) {
	repo, err := git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: spec.RepoID.String()})
	if err != nil {
		return nil, fmt.Errorf("resolve repository: %w", err)
	}
	target := repo.GetRepo().GetDefaultBranch()

	branches, err := git.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: spec.RepoID.String()})
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	found := false
	for _, b := range branches.GetRefs() {
		if b.GetName() == spec.Branch {
			found = true
		}
	}
	if !found {
		return nil, nil
	}
	diff, err := git.GetDiff(ctx, &gitv1.GetDiffRequest{
		Repo: spec.RepoID.String(), From: target, To: spec.Branch, MergeBase: true,
	})
	if err != nil {
		return nil, fmt.Errorf("diff %s against %s: %w", spec.Branch, target, err)
	}
	if strings.TrimSpace(diff.GetUnified()) == "" {
		return nil, nil
	}

	open, err := reviews.ListRuns(ctx, &reviewsv1.ListRunsRequest{RepoId: spec.RepoID.String(), State: "open"})
	if err != nil {
		return nil, fmt.Errorf("list open runs: %w", err)
	}
	for _, r := range open.GetRuns() {
		if r.GetSourceRef() == spec.Branch {
			return r, nil
		}
	}

	title := spec.Goal
	if spec.WorkItemKey != "" {
		title = spec.WorkItemKey + ": " + spec.Goal
	}
	if len(title) > titleLimit {
		title = title[:titleLimit-1] + "…"
	}
	req := &reviewsv1.CreateRunRequest{
		RepoId: spec.RepoID.String(), Title: title,
		SourceRef: spec.Branch, TargetRef: target,
		AuthorKind: "agent", AgentName: spec.AgentName, ModelName: spec.ModelName,
	}
	if spec.WorkItemID != uuid.Nil {
		req.WorkItemId = spec.WorkItemID.String()
	}
	if spec.AgentID != uuid.Nil {
		req.AuthorId = spec.AgentID.String()
	}
	created, err := reviews.CreateRun(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("open engineering run: %w", err)
	}
	for i, criterion := range spec.Acceptance {
		if _, err := reviews.AddPlanStep(ctx, &reviewsv1.AddPlanStepRequest{
			RunId: created.GetRun().GetId(), Ordinal: int32(i + 1), Text: criterion, State: "done",
		}); err != nil {
			return created.GetRun(), fmt.Errorf("record plan step %d: %w", i+1, err)
		}
	}
	return created.GetRun(), nil
}
