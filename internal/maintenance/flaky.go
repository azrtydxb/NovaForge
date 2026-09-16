package maintenance

import (
	"context"
	"fmt"
	"sort"
)

// scanFlaky detects the flaky_test kind: a test that both passed and
// failed AT THE SAME COMMIT SHA across the historical job results in
// in.JobResults. A test that fails on every run (however many distinct
// commits that spans) is a plain failure, not flakiness, and is
// deliberately not reported here.
func scanFlaky(ctx context.Context, in ScanInput) ([]Finding, error) {
	if len(in.JobResults) == 0 {
		return nil, nil
	}

	type shaKey struct{ test, sha string }
	mixed := make(map[shaKey]struct{ pass, fail bool })
	for _, r := range in.JobResults {
		k := shaKey{r.TestName, r.CommitSHA}
		v := mixed[k]
		if r.Passed {
			v.pass = true
		} else {
			v.fail = true
		}
		mixed[k] = v
	}

	flakyTests := make(map[string]bool)
	for k, v := range mixed {
		if v.pass && v.fail {
			flakyTests[k.test] = true
		}
	}

	names := make([]string, 0, len(flakyTests))
	for name := range flakyTests {
		names = append(names, name)
	}
	sort.Strings(names)

	findings := make([]Finding, 0, len(names))
	for _, name := range names {
		findings = append(findings, Finding{
			Kind:         "flaky_test",
			Title:        fmt.Sprintf("%s is flaky", name),
			Detail:       fmt.Sprintf("%s both passed and failed at the same commit in recent CI test observations", name),
			Severity:     "medium",
			Paths:        []string{name},
			ProposedType: "tech_debt",
		})
	}
	return findings, nil
}
