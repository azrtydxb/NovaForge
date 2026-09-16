package gates

import (
	"context"
	"fmt"
	"math"

	"github.com/novaforge/novaforge/internal/analysis"
)

// runTests evaluates the tests gate: `go test` must pass, and coverage must
// reach the minimum_coverage param (default 0, so an unconfigured gate never
// fails on coverage alone).
func runTests(ctx context.Context, in Input) (Evaluation, error) {
	if !analysis.IsGoModule(in.WorkDir) {
		return notGo(in, "tests")
	}
	if raw, exists := in.Params["minimum_coverage"]; exists {
		switch raw.(type) {
		case float64, int, int64:
		default:
			return toolError(in, "tests", fmt.Errorf("minimum_coverage must be a number"))
		}
	}
	minCoverage := paramFloat(in.Params, "minimum_coverage", 0)
	if math.IsNaN(minCoverage) || math.IsInf(minCoverage, 0) || minCoverage < 0 || minCoverage > 100 {
		return toolError(in, "tests", fmt.Errorf("minimum_coverage must be between 0 and 100"))
	}
	res, err := analysis.Tests(ctx, in.Exec, in.WorkDir)
	if err != nil {
		return toolError(in, "tests", err)
	}
	if !res.Passed {
		return newEvaluation(in, "tests", "fail", "tests failed:\n"+res.Output), nil
	}
	if !res.CoverageAvailable {
		if minCoverage > 0 {
			return toolError(in, "tests", fmt.Errorf("coverage unavailable: no executable statements"))
		}
		return newEvaluation(in, "tests", "pass", "tests pass; coverage unavailable: no executable statements"), nil
	}
	eval := newEvaluation(in, "tests", "pass", fmt.Sprintf("tests pass, coverage %.1f%%", res.Coverage))
	eval.CoveragePercent = &res.Coverage
	if res.Coverage < minCoverage {
		eval.Status = "fail"
		eval.Detail = fmt.Sprintf("coverage %.1f%% below minimum %.1f%%", res.Coverage, minCoverage)
	}
	return eval, nil
}
