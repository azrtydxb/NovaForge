// Package gates additionally holds the seven gate runners: the functions
// that actually evaluate one gate against one run's workspace, invoked by
// the controller (controller.go) after resolving which gates apply
// (resolve.go).
package gates

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/analysis"
)

// Input is what a GateRunner needs to evaluate one gate for one run.
type Input struct {
	OrgID, RepoID, RunID uuid.UUID
	WorkDir              string
	TargetSHA, SourceSHA string
	// PolicySHA is the immutable target commit used to resolve gate policy.
	PolicySHA string
	Params    map[string]any

	// Exec runs the analysis tools the gates use. It is injected so a test
	// can drive a gate without a toolchain, but the checks themselves live in
	// internal/analysis and are tested against the real tools there.
	Exec analysis.Exec
	// SASTRules is the semgrep ruleset the security gate runs; empty means
	// analysis.DefaultSASTRules, where the image puts it.
	SASTRules string
}

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
		RepoID:    in.RepoID,
		RunID:     in.RunID,
		Gate:      gate,
		Status:    status,
		Detail:    detail,
		TargetSHA: in.TargetSHA,
	}
}

// toolError turns a check that could not run into the gate's "error" status.
// It is never a pass: a gate whose tool is missing has not established that
// the change is fine.
func toolError(in Input, gate string, err error) (Evaluation, error) {
	return newEvaluation(in, gate, "error", fmt.Sprintf("%s check could not run: %v", gate, err)), nil
}

// notGo reports a Go-only gate evaluated on a repository that is not a Go
// module. "skipped" says the gate did not apply, which is true; "pass" would
// claim the change was checked. The controller still folds "skipped" into
// not-allowed, deliberately: a repository that *requires* a gate which cannot
// read it has a requirement that cannot be met, and the fix is to change the
// requirement in .novaforge/gates, not to let the merge through unchecked.
func notGo(in Input, gate string) (Evaluation, error) {
	return newEvaluation(in, gate, "skipped", "not a Go module; this gate checks Go code only"), nil
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
