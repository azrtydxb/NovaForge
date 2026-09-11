// Package gates additionally holds the seven gate runners: the functions
// that actually evaluate one gate against one run's workspace, invoked by
// the controller (controller.go) after resolving which gates apply
// (resolve.go).
package gates

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Input is what a GateRunner needs to evaluate one gate for one run.
type Input struct {
	OrgID, RepoID, RunID uuid.UUID
	WorkDir              string
	TargetSHA, SourceSHA string
	Params               map[string]any
	Proc                 ProcoderRunner
}

// ProcoderRunner invokes the procoder binary inside the job image with args,
// rooted at workdir, and returns its stdout, exit code, and any error
// launching it. err is reserved for a failure to invoke the binary at all
// (a transport or launch failure); a non-zero exit from a binary that did
// run is reported through exitCode, not err.
type ProcoderRunner func(ctx context.Context, workdir string, args ...string) (stdout []byte, exitCode int, err error)

// GateRunner evaluates one gate for one run and returns its result. A
// GateRunner never returns a non-nil error paired with a status of "pass":
// any failure to run the gate at all is reported as status "error" in the
// returned Evaluation instead, so the controller's deny-by-default fold sees
// a concrete (non-passing) result rather than having to interpret an error.
type GateRunner func(ctx context.Context, in Input) (Evaluation, error)

// Runners maps every one of the seven fixed gate names (see knownGates in
// resolve.go) to the runner that evaluates it.
var Runners = map[string]GateRunner{
	"tests":             runTests,
	"architecture":      runArchitecture,
	"security":          runSecurity,
	"api-compatibility": runAPICompat,
	"dependencies":      runDeps,
	"quality":           runQuality,
	"documentation":     runDocs,
}

// newEvaluation builds the Evaluation a runner returns, carrying the run and
// target SHA identity that the controller and store need.
func newEvaluation(in Input, gate, status, detail string) Evaluation {
	return Evaluation{
		OrgID:     in.OrgID,
		RunID:     in.RunID,
		Gate:      gate,
		Status:    status,
		Detail:    detail,
		TargetSHA: in.TargetSHA,
	}
}

// runProcExitGate is the shared shape for gates that just invoke procoder
// with args and turn a non-zero exit into "fail" and a launch error into
// "error": dependencies, quality, and documentation.
func runProcExitGate(ctx context.Context, in Input, gate string, args ...string) (Evaluation, error) {
	stdout, exitCode, err := in.Proc(ctx, in.WorkDir, args...)
	if err != nil {
		return newEvaluation(in, gate, "error", fmt.Sprintf("procoder %s: %v", strings.Join(args, " "), err)), nil
	}
	if exitCode != 0 {
		return newEvaluation(in, gate, "fail", fmt.Sprintf("procoder %s exited %d: %s", strings.Join(args, " "), exitCode, strings.TrimSpace(string(stdout)))), nil
	}
	return newEvaluation(in, gate, "pass", strings.TrimSpace(string(stdout))), nil
}

// paramFloat reads a numeric parameter from a gate's Params, defaulting to
// def when absent. Params come from YAML decoded into map[string]any, so the
// concrete numeric type varies by how the value was written.
func paramFloat(params map[string]any, key string, def float64) float64 {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return def
}

// paramStringSlice reads a string-slice parameter from a gate's Params.
// YAML-decoded sequences arrive as []any, so each element is type-asserted
// individually rather than assuming []string.
func paramStringSlice(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	if s, ok := v.([]string); ok {
		return s
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
