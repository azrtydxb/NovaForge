package maintenance

import (
	"context"
	"fmt"

	"github.com/novaforge/novaforge/internal/gates"
)

// scanArchitecture detects the architectural_violation kind by reusing the
// architecture gate's own import-graph runner (gates.Runners["architecture"])
// against in.WorkDir — a checkout of the repository's default branch,
// per the spec's "against the default branch" — rather than
// reimplementing import-graph analysis a second time.
//
// An empty WorkDir means there is nothing checked out to analyze —
// reported as no findings, not an error.
func scanArchitecture(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.WorkDir == "" {
		return nil, nil
	}

	runner, ok := gates.Runners["architecture"]
	if !ok {
		return nil, fmt.Errorf("maintenance: architecture scan: no architecture gate runner registered")
	}

	eval, err := runner(ctx, gates.Input{
		OrgID:     in.OrgID,
		RepoID:    in.RepoID,
		WorkDir:   in.WorkDir,
		TargetSHA: in.TargetRef,
		Params:    in.ArchParams,
		Exec:      in.Exec,
	})
	if err != nil {
		return nil, fmt.Errorf("maintenance: architecture scan: %w", err)
	}
	if eval.Status != "fail" {
		return nil, nil
	}

	return []Finding{{
		Kind:         "architectural_violation",
		Title:        "forbidden dependency detected on the default branch",
		Detail:       eval.Detail,
		Severity:     "high",
		ProposedType: "architecture",
	}}, nil
}
