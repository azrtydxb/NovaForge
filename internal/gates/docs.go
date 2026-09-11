package gates

import "context"

// runDocs evaluates the documentation gate by invoking procoder's "docs"
// command, mapping a non-zero exit to "fail" and a launch error to "error".
func runDocs(ctx context.Context, in Input) (Evaluation, error) {
	return runProcExitGate(ctx, in, "documentation", "docs")
}
