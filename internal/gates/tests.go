package gates

import (
	"context"
	"fmt"

	"github.com/novaforge/novaforge/internal/analysis"
)

// runTests evaluates the tests gate: `go test` must pass, and coverage must
// reach the minimum_coverage param (default 0, so an unconfigured gate never
// fails on coverage alone).
func runTests(ctx context.Context, in Input) (Evaluation, error) {
	if !analysis.IsGoModule(in.WorkDir) {
		return notGo(in, "tests")
	}
	res, err := analysis.Tests(ctx, in.Exec, in.WorkDir)
	if err != nil {
		return toolError(in, "tests", err)
	}
	if !res.Passed {
		return newEvaluation(in, "tests", "fail", "tests failed:\n"+res.Output), nil
	}
	minCoverage := paramFloat(in.Params, "minimum_coverage", 0)
	if res.Coverage < minCoverage {
		return newEvaluation(in, "tests", "fail",
			fmt.Sprintf("coverage %.1f%% below minimum %.1f%%", res.Coverage, minCoverage)), nil
	}
	return newEvaluation(in, "tests", "pass", fmt.Sprintf("tests pass, coverage %.1f%%", res.Coverage)), nil
}
