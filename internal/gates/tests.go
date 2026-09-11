package gates

import (
	"context"
	"encoding/json"
	"fmt"
)

// testsReport is the structured JSON procoder's "test" command prints to
// stdout: overall coverage percentage.
type testsReport struct {
	Coverage float64 `json:"coverage"`
}

// runTests evaluates the tests gate by invoking procoder's "test" command
// and comparing reported coverage against the minimum_coverage param
// (defaulting to 0, so an unconfigured gate never fails on coverage alone).
func runTests(ctx context.Context, in Input) (Evaluation, error) {
	stdout, exitCode, err := in.Proc(ctx, in.WorkDir, "test")
	if err != nil {
		return newEvaluation(in, "tests", "error", fmt.Sprintf("procoder test: %v", err)), nil
	}

	var report testsReport
	if jsonErr := json.Unmarshal(stdout, &report); jsonErr != nil {
		return newEvaluation(in, "tests", "error", fmt.Sprintf("parse procoder test output: %v", jsonErr)), nil
	}

	if exitCode != 0 {
		return newEvaluation(in, "tests", "fail", fmt.Sprintf("procoder test exited %d", exitCode)), nil
	}

	minCoverage := paramFloat(in.Params, "minimum_coverage", 0)
	if report.Coverage < minCoverage {
		return newEvaluation(in, "tests", "fail",
			fmt.Sprintf("coverage %.1f%% below minimum %.1f%%", report.Coverage, minCoverage)), nil
	}
	return newEvaluation(in, "tests", "pass", fmt.Sprintf("coverage %.1f%%", report.Coverage)), nil
}
