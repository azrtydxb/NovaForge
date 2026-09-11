package maintenance

import (
	"context"
	"fmt"
)

// defaultRegressionThresholdPercent is used when
// ScanInput.RegressionThresholdPercent is unset.
const defaultRegressionThresholdPercent = 10.0

// scanPerf detects the performance_regression kind: a benchmark whose
// latest value regressed more than the configured percentage over its
// baseline. Benchmarks are assumed "lower is better" (latency,
// allocations, ...), matching how procoder's benchmark artifacts are
// recorded.
func scanPerf(ctx context.Context, in ScanInput) ([]Finding, error) {
	if len(in.Benchmarks) == 0 {
		return nil, nil
	}

	threshold := in.RegressionThresholdPercent
	if threshold <= 0 {
		threshold = defaultRegressionThresholdPercent
	}

	var findings []Finding
	for _, b := range in.Benchmarks {
		if b.Baseline <= 0 {
			continue
		}
		regressionPct := (b.Latest - b.Baseline) / b.Baseline * 100
		if regressionPct < threshold {
			continue
		}
		findings = append(findings, Finding{
			Kind:         "performance_regression",
			Title:        fmt.Sprintf("%s regressed %.1f%% (%.4g -> %.4g)", b.Name, regressionPct, b.Baseline, b.Latest),
			Detail:       fmt.Sprintf("regression of %.1f%%, at or above the %.1f%% threshold", regressionPct, threshold),
			Severity:     perfSeverity(regressionPct),
			Paths:        []string{b.Name},
			ProposedType: "tech_debt",
		})
	}
	return findings, nil
}

func perfSeverity(regressionPct float64) string {
	switch {
	case regressionPct >= 50:
		return "high"
	case regressionPct >= 25:
		return "medium"
	default:
		return "low"
	}
}
