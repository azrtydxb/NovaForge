// Package maintenance implements autonomous maintenance: eight scanners
// that each detect one condition worth an engineer's attention (outdated
// dependencies, CVEs, flaky tests, dead code, coverage regression,
// documentation drift, performance regression, and architectural
// violations), and a proposer (propose.go) that turns their findings into
// Work Items for human or agent approval — never an unapproved fix.
package maintenance

import (
	"context"
	"sort"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/graph"
)

// Finding is one thing a scanner detected worth turning into a Work Item.
type Finding struct {
	// Kind is one of the eight fixed scanner kinds: outdated_dependency,
	// cve, flaky_test, dead_code, coverage_regression, documentation_drift,
	// performance_regression, or architectural_violation.
	Kind string
	// Title is a short, human-readable summary.
	Title string
	// Detail expands on Title with whatever evidence the scanner has.
	Detail string
	// Severity is one of critical, high, medium, or low.
	Severity string
	// Paths names the files or symbols the finding concerns, if any.
	Paths []string
	// ProposedType is the work.Item type Propose (Task 6) should use when
	// turning this finding into a Work Item.
	ProposedType string
}

// JobResult is one historical CI job result for one test, at one commit —
// the flaky-test scanner's raw material.
type JobResult struct {
	TestName  string
	CommitSHA string
	Passed    bool
}

// CoverageSample is the coverage-regression scanner's raw material: the
// latest tests-gate coverage percentage for a repository, compared against
// its previous evaluation. Either pointer may be nil, meaning "no
// evaluation available yet" — the scanner reports nothing rather than
// guessing.
type CoverageSample struct {
	Previous *float64
	Latest   *float64
	// MinDropPercent is the minimum percentage-point drop that counts as a
	// regression. Zero (the default) is treated as 1.0: coverage jitters by
	// less than a point on almost every run, so nothing would ever be
	// stable enough to leave alone at a literal zero threshold.
	MinDropPercent float64
}

// BenchmarkResult is the performance-regression scanner's raw material:
// one named benchmark's baseline value against its latest, assumed to be a
// "lower is better" measurement (e.g. latency, allocations) — Latest
// higher than Baseline by more than the configured percentage is a
// regression.
type BenchmarkResult struct {
	Name     string
	Baseline float64
	Latest   float64
}

// ContextDocRef is one context document (from a repository's
// .novaforge/context directory) and the symbol keys it references — the
// documentation-drift scanner's raw material. Extracting which symbols a
// document references is context assembly's job, not this scanner's; it
// only checks whether the referenced symbols still exist in the graph.
type ContextDocRef struct {
	Path              string
	ReferencedSymbols []string
}

// ScanInput is what a Scanner needs to evaluate one repository. No single
// scanner needs every field: each guards on the fields it actually uses
// being present and reports no findings (not an error) when its own
// raw material was never supplied — see Scanner FAILURE ISOLATION below.
type ScanInput struct {
	OrgID, RepoID uuid.UUID
	// WorkDir is a local checkout of the repository's default branch, used
	// by the dependency, CVE, and architecture scanners.
	WorkDir string
	// TargetRef is the branch the architecture scanner checks — normally
	// the repository's default branch, per the spec's
	// "against the default branch".
	TargetRef string

	// Exec runs the analysis tools the dependency, CVE and architecture
	// scanners use (see internal/analysis).
	Exec analysis.Exec
	// ArchParams carries the architecture gate's forbidden_dependencies
	// parameter (and any other gates.Definition.Params the architecture
	// runner understands), reused unchanged by the architectural-violation
	// scanner.
	ArchParams map[string]any
	// Git is unused directly by any scanner today (the architecture
	// scanner walks WorkDir on disk via golang.org/x/tools/go/packages,
	// not gRPC) but is threaded through ScanInput for callers that resolve
	// WorkDir by checking it out via Git first.
	Git gitv1.GitServiceClient

	// Graph backs the dead-code and documentation-drift scanners.
	Graph *graph.Store

	// JobResults backs the flaky-test scanner.
	JobResults []JobResult

	// Coverage backs the coverage-regression scanner.
	Coverage CoverageSample

	// Benchmarks backs the performance-regression scanner.
	Benchmarks []BenchmarkResult
	// RegressionThresholdPercent is the minimum percentage regression that
	// counts. Zero (the default) is treated as 10.
	RegressionThresholdPercent float64

	// ContextDocs backs the documentation-drift scanner.
	ContextDocs []ContextDocRef
}

// Scanner detects one condition and reports every instance it finds. A
// Scanner returning a non-nil error means it could not run at all (a
// launch failure, a database error, ...) — never a status it encodes as a
// Finding, since RunAll (and Task 5's controller) uses the error to decide
// whether the scanner ran, not what it found.
type Scanner func(ctx context.Context, in ScanInput) ([]Finding, error)

// Scanners maps every one of the eight fixed finding kinds to the scanner
// that detects it.
var Scanners = map[string]Scanner{
	"architectural_violation": scanArchitecture,
	"coverage_regression":     scanCoverage,
	"cve":                     scanCVE,
	"dead_code":               scanDeadCode,
	"documentation_drift":     scanDocs,
	"flaky_test":              scanFlaky,
	"outdated_dependency":     scanOutdatedDeps,
	"performance_regression":  scanPerf,
}

// SortedKinds returns the keys of Scanners in sorted order.
func SortedKinds() []string {
	kinds := make([]string, 0, len(Scanners))
	for k := range Scanners {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// nullableUUID turns uuid.Nil into a nil any, so it binds to a nullable
// ::uuid query parameter as SQL NULL rather than the all-zero UUID —
// letting a scanner's repo-scoping predicate be optional ("every repo in
// the org" when RepoID is unset) without a second query shape.
func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// RunAll runs every scanner in Scanners against in, in a fixed (sorted)
// order, isolating each scanner's failure from the rest: a scanner that
// returns an error is reported through onError (if non-nil) and skipped —
// it never prevents the remaining seven scanners from running and
// reporting their own findings.
func RunAll(ctx context.Context, in ScanInput, onError func(kind string, err error)) []Finding {
	var all []Finding
	for _, kind := range SortedKinds() {
		findings, err := Scanners[kind](ctx, in)
		if err != nil {
			if onError != nil {
				onError(kind, err)
			}
			continue
		}
		all = append(all, findings...)
	}
	return all
}
