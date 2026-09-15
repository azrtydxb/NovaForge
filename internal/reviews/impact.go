package reviews

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
)

// Impact summarizes the size and shape of the diff between two refs. The
// edge renders this as the CHANGE IMPACT block of an Engineering Run.
type Impact struct {
	FilesChanged int
	Insertions   int
	Deletions    int
	Paths        []string
}

// ComputeImpact fetches the diff of to since it diverged from from, for
// repoID, through git, and parses it line-wise into an Impact summary.
//
// The diff is taken from the merge base. It used to be from..to, which
// compares the branch with the target as the target is now: every change that
// landed on the target after the branch was cut was counted as the branch's,
// in reverse — so a two-file change could read as a twenty-file one, and
// auto-merge's size cap was judged against changes the run never made.
func ComputeImpact(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, from, to string) (Impact, error) {
	resp, err := git.GetDiff(ctx, &gitv1.GetDiffRequest{
		Repo:           repoID.String(),
		From:           from,
		To:             to,
		SinceMergeBase: true,
	})
	if err != nil {
		return Impact{}, fmt.Errorf("get diff: %w", err)
	}

	var impact Impact
	lines := strings.Split(resp.Unified, "\n")
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			impact.FilesChanged++
			if path, ok := extractBPath(line); ok {
				impact.Paths = append(impact.Paths, path)
			}
		case strings.HasPrefix(line, "+++"):
			// header line, not a content line
		case strings.HasPrefix(line, "---"):
			// header line, not a content line
		case strings.HasPrefix(line, "+"):
			impact.Insertions++
		case strings.HasPrefix(line, "-"):
			impact.Deletions++
		}
	}
	return impact, nil
}

// extractBPath extracts the path from the "b/" side of a
// "diff --git a/<path> b/<path>" header line.
func extractBPath(line string) (string, bool) {
	idx := strings.Index(line, " b/")
	if idx == -1 {
		return "", false
	}
	return line[idx+len(" b/"):], true
}

// Risk thresholds. They are deliberately few and stated, because a risk level
// is only useful to a reviewer who can see why it was given.
const (
	highRiskFiles = 20
	highRiskLines = 1000
	medRiskFiles  = 5
	medRiskLines  = 200
)

// sensitivePrefixes are paths whose change carries risk regardless of size:
// the platform's own governance, schemas and deployment, and dependencies.
var sensitivePrefixes = []string{
	".novaforge/", "deploy/", "migrations/", "proto/",
}

var sensitiveNames = []string{
	"go.mod", "go.sum", "package.json", "package-lock.json", "Dockerfile",
}

// AssessRisk rates an Impact low, medium or high and says which rule decided
// it. The level is derived only from the diff — size and the paths touched —
// never from anything a model said about the change.
func AssessRisk(im Impact) (string, []string) {
	lines := im.Insertions + im.Deletions
	var sensitive []string
	for _, p := range im.Paths {
		if isSensitive(p) {
			sensitive = append(sensitive, p)
		}
	}
	switch {
	case len(sensitive) > 0:
		return "high", []string{fmt.Sprintf("touches %s", strings.Join(sensitive, ", "))}
	case im.FilesChanged > highRiskFiles:
		return "high", []string{fmt.Sprintf("%d files changed (more than %d)", im.FilesChanged, highRiskFiles)}
	case lines > highRiskLines:
		return "high", []string{fmt.Sprintf("%d lines changed (more than %d)", lines, highRiskLines)}
	case im.FilesChanged > medRiskFiles:
		return "medium", []string{fmt.Sprintf("%d files changed (more than %d)", im.FilesChanged, medRiskFiles)}
	case lines > medRiskLines:
		return "medium", []string{fmt.Sprintf("%d lines changed (more than %d)", lines, medRiskLines)}
	default:
		return "low", []string{fmt.Sprintf("%d files and %d lines changed, no governance, schema, deployment or dependency file", im.FilesChanged, lines)}
	}
}

func isSensitive(path string) bool {
	for _, pre := range sensitivePrefixes {
		if strings.HasPrefix(path, pre) || strings.Contains(path, "/"+pre) {
			return true
		}
	}
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	for _, n := range sensitiveNames {
		if base == n {
			return true
		}
	}
	return false
}

// GetRunImpact measures a run's change through the git service.
func (g *GRPCServer) GetRunImpact(ctx context.Context, req *reviewsv1.GetRunImpactRequest) (*reviewsv1.GetRunImpactResponse, error) {
	run, err := g.resolveRun(ctx, req.GetRunId(), req.GetRepoId(), req.GetNumber())
	if err != nil {
		return nil, err
	}
	if g.Git == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment has no git service wired to measure change impact")
	}
	im, err := ComputeImpact(ctx, g.Git, run.OrgID, run.RepoID, run.TargetRef, run.SourceRef)
	if err != nil {
		if status.Code(err) == codes.NotFound || strings.Contains(err.Error(), "NotFound") {
			return nil, status.Errorf(codes.NotFound, "measure change impact: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "measure change impact: %v", err)
	}
	risk, reasons := AssessRisk(im)
	paths := im.Paths
	if paths == nil {
		paths = []string{}
	}
	return &reviewsv1.GetRunImpactResponse{
		FilesChanged: int32(im.FilesChanged), Insertions: int32(im.Insertions), Deletions: int32(im.Deletions),
		Paths: paths, Risk: risk, RiskReasons: reasons,
	}, nil
}
