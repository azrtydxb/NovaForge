package gates

import "context"

// runDeps evaluates the dependencies gate by invoking procoder's "deps"
// command, mapping a non-zero exit to "fail" and a launch error to "error".
func runDeps(ctx context.Context, in Input) (Evaluation, error) {
	return runProcExitGate(ctx, in, "dependencies", "deps")
}
