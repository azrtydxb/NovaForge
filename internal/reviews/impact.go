package reviews

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// Impact summarizes the size and shape of the diff between two refs. The
// edge renders this as the CHANGE IMPACT block of an Engineering Run.
type Impact struct {
	FilesChanged int
	Insertions   int
	Deletions    int
	Paths        []string
}

// ComputeImpact fetches the unified diff between from and to for repoID
// through git and parses it line-wise into an Impact summary.
func ComputeImpact(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, from, to string) (Impact, error) {
	resp, err := git.GetDiff(ctx, &gitv1.GetDiffRequest{
		Repo: repoID.String(),
		From: from,
		To:   to,
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
