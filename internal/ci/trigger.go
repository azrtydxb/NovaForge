package ci

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// SetScheduler gives the query server the scheduler TriggerRun needs. It is
// set after construction because the scheduler and the query server are
// built from the same Service and would otherwise be a construction cycle.
func (q *QueryServer) SetScheduler(s *Scheduler, git gitv1.GitServiceClient) {
	q.scheduler = s
	q.git = git
}

// TriggerRun schedules a run of the workflow at ref on demand. A push and a
// request schedule through the same code (Scheduler.ScheduleRun), so CI
// cannot behave differently depending on which asked. Asking twice for the
// same commit returns the run that already exists rather than a second one.
func (q *QueryServer) TriggerRun(ctx context.Context, req *civ1.TriggerRunRequest) (*civ1.TriggerRunResponse, error) {
	sc, err := q.scope(ctx)
	if err != nil {
		return nil, err
	}
	if q.scheduler == nil || q.git == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment has no scheduler wired to trigger runs")
	}
	repoID, err := uuid.Parse(req.GetRepoId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid repo id")
	}

	repo, err := q.git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: repoID.String()})
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "resolve repository: %v", err)
	}
	ref := req.GetRef()
	if ref == "" {
		ref = repo.GetRepo().GetDefaultBranch()
	}

	sha, err := q.resolveRefSHA(ctx, repoID, ref)
	if err != nil {
		return nil, err
	}

	run, err := q.scheduler.ScheduleRun(ctx, sc.OrgID, repoID, repo.GetRepo().GetName(), ref, sha)
	if err != nil {
		// "Nothing happened" needs a reason when a person asked for it; the
		// push consumer is the one that treats these as ordinary and silent.
		if errors.Is(err, ErrNoWorkflow) {
			return nil, status.Errorf(codes.FailedPrecondition, "%s at %s declares no CI workflow", repo.GetRepo().GetName(), ref)
		}
		if errors.Is(err, ErrInvalidWorkflow) {
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "schedule run: %v", err)
	}
	return &civ1.TriggerRunResponse{Run: runSummary(run)}, nil
}

// resolveRefSHA turns a branch, tag, or commit into the commit sha a run is
// pinned to, so a run always records exactly what was tested.
func (q *QueryServer) resolveRefSHA(ctx context.Context, repoID uuid.UUID, ref string) (string, error) {
	commits, err := q.git.ListCommits(ctx, &gitv1.ListCommitsRequest{
		Repo: repoID.String(), Ref: ref, Limit: 1,
	})
	if err != nil {
		return "", status.Errorf(codes.NotFound, "unknown ref %q: %v", ref, err)
	}
	if len(commits.GetCommits()) == 0 {
		return "", status.Errorf(codes.NotFound, "ref %q has no commits", ref)
	}
	sha := strings.TrimSpace(commits.GetCommits()[0].GetSha())
	if sha == "" {
		return "", status.Error(codes.Internal, fmt.Sprintf("ref %q resolved to an empty commit sha", ref))
	}
	return sha, nil
}
