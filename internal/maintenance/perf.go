package maintenance

import (
	"context"
	"fmt"
	"math"
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
		if math.IsNaN(b.Baseline) || math.IsInf(b.Baseline, 0) || math.IsNaN(b.Latest) || math.IsInf(b.Latest, 0) || b.Latest < 0 {
			return nil, fmt.Errorf("benchmark %s has invalid measurements", b.Name)
		}
		if b.Baseline <= 0 {
			continue
		}
		regressionPct := (b.Latest - b.Baseline) / b.Baseline * 100
		if math.IsInf(regressionPct, 0) || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
			return nil, fmt.Errorf("benchmark %s percentage comparison unavailable", b.Name)
		}
		if regressionPct < threshold {
			continue
		}
		detail := fmt.Sprintf("regression of %.1f%%, at or above the %.1f%% threshold", regressionPct, threshold)
		if b.BaselineRun != "" && b.LatestRun != "" {
			detail += fmt.Sprintf("; CI baseline run %s, latest run %s", b.BaselineRun, b.LatestRun)
		}
		findings = append(findings, Finding{
			Kind:         "performance_regression",
			Title:        fmt.Sprintf("%s regressed %.1f%% (%.4g -> %.4g)", b.Name, regressionPct, b.Baseline, b.Latest),
			Detail:       detail,
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
