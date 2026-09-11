package maintenance

import (
	"context"
	"fmt"
)

// scanCoverage detects the coverage_regression kind by comparing the
// latest tests-gate coverage percentage against the previous evaluation
// for the same repository. Resolving those two numbers (typically from
// gates.Store's evaluation history) is the caller's job — ScanInput.Coverage
// carries them in already — so this scanner stays a pure comparison with
// no database access of its own.
//
// Either Previous or Latest being nil means no comparison is possible yet
// (e.g. the first evaluation for a repository): reported as no findings,
// not an error.
func scanCoverage(ctx context.Context, in ScanInput) ([]Finding, error) {
	prev, latest := in.Coverage.Previous, in.Coverage.Latest
	if prev == nil || latest == nil {
		return nil, nil
	}

	threshold := in.Coverage.MinDropPercent
	if threshold <= 0 {
		threshold = 1.0
	}

	drop := *prev - *latest
	if drop < threshold {
		return nil, nil
	}

	severity := "low"
	switch {
	case drop >= 10:
		severity = "high"
	case drop >= 5:
		severity = "medium"
	}

	return []Finding{{
		Kind:         "coverage_regression",
		Title:        fmt.Sprintf("coverage dropped from %.1f%% to %.1f%%", *prev, *latest),
		Detail:       fmt.Sprintf("a %.1f point drop, at or above the %.1f point threshold", drop, threshold),
		Severity:     severity,
		ProposedType: "tech_debt",
	}}, nil
}
