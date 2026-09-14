package gates

import (
	"context"
	"fmt"
	"strings"

	"github.com/novaforge/novaforge/internal/analysis"
)

// runDeps evaluates the dependencies gate: no dependency with a known
// published advisory. It covers every ecosystem osv-scanner reads, so it
// applies to repositories that are not Go.
func runDeps(ctx context.Context, in Input) (Evaluation, error) {
	vulns, err := analysis.Vulnerabilities(ctx, in.Exec, in.WorkDir)
	if err != nil {
		return toolError(in, "dependencies", err)
	}
	if len(vulns) == 0 {
		return newEvaluation(in, "dependencies", "pass", "no dependency with a known advisory"), nil
	}
	var b strings.Builder
	for _, v := range vulns {
		fmt.Fprintf(&b, "%s %s (%s): %s\n", v.Package, v.Version, v.Ecosystem, strings.Join(v.IDs, ", "))
	}
	return newEvaluation(in, "dependencies", "fail",
		fmt.Sprintf("%d dependencies with known advisories:\n%s", len(vulns), strings.TrimRight(b.String(), "\n"))), nil
}
