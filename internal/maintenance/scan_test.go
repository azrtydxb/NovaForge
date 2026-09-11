package maintenance_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/novaforge/novaforge/internal/maintenance"
)

func TestScannersCoverAllEightKinds(t *testing.T) {
	want := []string{
		"architectural_violation",
		"coverage_regression",
		"cve",
		"dead_code",
		"documentation_drift",
		"flaky_test",
		"outdated_dependency",
		"performance_regression",
	}
	got := maintenance.SortedKinds()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want kinds %v, got %v", want, got)
	}
	if len(maintenance.Scanners) != 8 {
		t.Fatalf("want 8 scanners, got %d", len(maintenance.Scanners))
	}
}

func TestCVEScannerProducesCriticalFinding(t *testing.T) {
	proc := func(ctx context.Context, workdir string, args ...string) ([]byte, int, error) {
		if len(args) == 0 || args[0] != "security" {
			return []byte(`{}`), 0, nil
		}
		return []byte(`{"advisories":[{"package":"github.com/example/vuln","severity":"critical","summary":"known RCE"}]}`), 0, nil
	}

	scanner := maintenance.Scanners["cve"]
	findings, err := scanner(context.Background(), maintenance.ScanInput{WorkDir: "/repo", Proc: proc})
	if err != nil {
		t.Fatalf("cve scanner: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Kind != "cve" {
		t.Fatalf("want kind cve, got %q", f.Kind)
	}
	if f.Severity != "critical" {
		t.Fatalf("want severity critical, got %q", f.Severity)
	}
}

func TestScannerFailureIsIsolated(t *testing.T) {
	proc := func(ctx context.Context, workdir string, args ...string) ([]byte, int, error) {
		if len(args) > 0 && args[0] == "security" {
			return nil, 0, fmt.Errorf("procoder security: launch failed")
		}
		return []byte(`{"outdated":[]}`), 0, nil
	}

	var failedKinds []string
	findings := maintenance.RunAll(context.Background(), maintenance.ScanInput{
		WorkDir: "/repo",
		Proc:    proc,
	}, func(kind string, err error) {
		failedKinds = append(failedKinds, kind)
	})

	if len(failedKinds) != 1 || failedKinds[0] != "cve" {
		t.Fatalf("want exactly the cve scanner to fail, got %v", failedKinds)
	}
	// The other seven scanners ran without their raw material configured,
	// so they report no findings, but crucially RunAll did not stop or
	// panic when the cve scanner failed.
	_ = findings
}
