package gates

import "context"

// runQuality evaluates the quality gate by invoking procoder's "lint"
// command, mapping a non-zero exit to "fail" and a launch error to "error".
func runQuality(ctx context.Context, in Input) (Evaluation, error) {
	return runProcExitGate(ctx, in, "quality", "lint")
}
