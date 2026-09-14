package gates

import (
	"context"
	"fmt"
	"strings"

	"github.com/novaforge/novaforge/internal/analysis"
)

// runSecurity evaluates the security gate: a secret scan over every
// repository, and static analysis over Go code. A secret fails the gate on its
// own — the spec marks a leaked secret as unconditionally blocking — and a
// finding's detail names the rule and location, never the secret, because a
// gate's detail is displayed.
func runSecurity(ctx context.Context, in Input) (Evaluation, error) {
	secrets, err := analysis.Secrets(ctx, in.Exec, in.WorkDir)
	if err != nil {
		return toolError(in, "security", err)
	}
	if len(secrets) > 0 {
		return newEvaluation(in, "security", "fail", "secrets found:\n"+list(secrets)), nil
	}

	if !analysis.IsGoModule(in.WorkDir) {
		return newEvaluation(in, "security", "pass",
			"no secrets found; static analysis not run (not a Go module)"), nil
	}
	sast, err := analysis.SAST(ctx, in.Exec, in.WorkDir, in.SASTRules)
	if err != nil {
		return toolError(in, "security", err)
	}
	if len(sast) > 0 {
		return newEvaluation(in, "security", "fail", "static analysis findings:\n"+list(sast)), nil
	}
	return newEvaluation(in, "security", "pass", "no secrets, no static analysis findings"), nil
}

// list renders findings one per line, capped so a gate detail stays readable.
func list(found []analysis.Finding) string {
	const max = 20
	var b strings.Builder
	for i, f := range found {
		if i == max {
			fmt.Fprintf(&b, "… and %d more\n", len(found)-max)
			break
		}
		fmt.Fprintf(&b, "%s\n", f)
	}
	return strings.TrimRight(b.String(), "\n")
}
