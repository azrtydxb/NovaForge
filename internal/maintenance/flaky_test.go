package maintenance_test

import (
	"context"
	"testing"

	"github.com/novaforge/novaforge/internal/maintenance"
)

func TestFlakyDetectedFromMixedResults(t *testing.T) {
	var results []maintenance.JobResult
	// Ten historical job results for one test, all at the same commit SHA,
	// split between pass and fail — nondeterminism at a fixed commit is
	// exactly what flakiness means.
	for i := 0; i < 10; i++ {
		results = append(results, maintenance.JobResult{
			TestName:  "TestFlakyThing",
			CommitSHA: "abc123",
			Passed:    i%2 == 0,
		})
	}

	scanner := maintenance.Scanners["flaky_test"]
	findings, err := scanner(context.Background(), maintenance.ScanInput{JobResults: results})
	if err != nil {
		t.Fatalf("flaky scanner: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 flaky finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Kind != "flaky_test" {
		t.Fatalf("want kind flaky_test, got %q", findings[0].Kind)
	}
	found := false
	for _, p := range findings[0].Paths {
		if p == "TestFlakyThing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want the finding to name TestFlakyThing, got paths %v", findings[0].Paths)
	}
}

func TestConsistentFailureIsNotFlaky(t *testing.T) {
	var results []maintenance.JobResult
	// The same test failing on every run, across several different
	// commits: a plain failure, not flakiness, since no single commit ever
	// produced both outcomes.
	shas := []string{"c1", "c2", "c3", "c4", "c5"}
	for _, sha := range shas {
		results = append(results, maintenance.JobResult{
			TestName:  "TestAlwaysFails",
			CommitSHA: sha,
			Passed:    false,
		})
	}

	scanner := maintenance.Scanners["flaky_test"]
	findings, err := scanner(context.Background(), maintenance.ScanInput{JobResults: results})
	if err != nil {
		t.Fatalf("flaky scanner: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want no flaky findings for a consistently failing test, got %+v", findings)
	}
}
