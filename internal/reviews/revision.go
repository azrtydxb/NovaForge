package reviews

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sourceHead resolves the run's source ref in the repository that ref actually
// lives in — the run's own for a branch run, the fork for a cross-fork run.
// Resolving it in run.RepoID regardless would have found nothing for a
// cross-fork run, and every revision-pinned check (the reviewed sha, the
// authorized merge pair) would then have been made against the wrong
// repository's history.
func sourceHead(ctx context.Context, git gitv1.GitServiceClient, run Run) (string, error) {
	return refHead(ctx, git, run.sourceRepo().String(), run.SourceRef)
}

// sourceRepo is the repository SourceRef lives in, defaulting to the run's own.
// The default is what makes every run that predates forks keep working: such a
// row has no source repository of its own, and its source ref is a branch of
// its own repository.
func (r Run) sourceRepo() uuid.UUID {
	if r.SourceRepoID == uuid.Nil {
		return r.RepoID
	}
	return r.SourceRepoID
}
func refHead(ctx context.Context, git gitv1.GitServiceClient, repo, ref string) (string, error) {
	if git == nil {
		return "", fmt.Errorf("git service is not configured")
	}
	commits, err := git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repo, Ref: ref, Limit: 1})
	if err != nil {
		return "", err
	}
	if len(commits.GetCommits()) != 1 || commits.GetCommits()[0].GetSha() == "" {
		return "", fmt.Errorf("ref %q has no commit", ref)
	}
	return commits.GetCommits()[0].GetSha(), nil
}

func (g *GRPCServer) ListReviews(ctx context.Context, req *reviewsv1.ListReviewsRequest) (*reviewsv1.ListReviewsResponse, error) {
	id, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	run, err := g.Store.GetRun(ctx, id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "run not found")
	}
	rows, err := g.Store.ListReviews(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list reviews: %v", err)
	}
	// Current refs are supplemental, not a prerequisite for durable history.
	head, available := currentSource(ctx, g.Git, run)
	out := &reviewsv1.ListReviewsResponse{CurrentSourceSha: head, CurrentSourceAvailable: available, Reviews: []*reviewsv1.Review{}}
	for _, r := range rows {
		out.Reviews = append(out.Reviews, &reviewsv1.Review{ReviewerId: r.ReviewerID.String(), ReviewerKind: r.ReviewerKind, Verdict: r.Verdict, Summary: r.Summary, SourceSha: r.SourceSHA, CreatedAt: r.CreatedAt.Format(rfc3339)})
	}
	return out, nil
}

// Revision currency is advisory on read surfaces. A hung Git connection must
// not indefinitely hide already-persisted evidence or the exceptions inbox.
func currentSource(ctx context.Context, git gitv1.GitServiceClient, run Run) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	head, err := sourceHead(ctx, git, run)
	return head, err == nil
}
