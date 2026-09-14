package maintenance_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/analysis"
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

// TestCVEScannerFindsAKnownVulnerability runs the real osv-scanner against a
// module pinned to golang.org/x/text v0.3.0, which carries published
// advisories. The scanner used to parse JSON from a procoder subcommand that
// never existed, so it had never produced a finding anywhere.
func TestCVEScannerFindsAKnownVulnerability(t *testing.T) {
	for _, tool := range []string{"go", "osv-scanner"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is not installed; this test runs the real tool (see deploy/docker/Dockerfile.analysis)", tool)
		}
	}
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/probe\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.0\n")
	write(t, dir, "main.go", "package probe\n\nimport _ \"golang.org/x/text/language\"\n")
	if out, err := exec.Command("go", "-C", dir, "mod", "tidy").CombinedOutput(); err != nil {
		t.Fatalf("tidy: %v: %s", err, out)
	}

	findings, err := maintenance.Scanners["cve"](context.Background(), maintenance.ScanInput{WorkDir: dir, Exec: analysis.DefaultExec})
	if err != nil {
		t.Fatalf("cve scanner: %v", err)
	}
	var hit *maintenance.Finding
	for i := range findings {
		if strings.Contains(findings[i].Title, "golang.org/x/text") {
			hit = &findings[i]
		}
	}
	if hit == nil {
		t.Fatalf("no finding for golang.org/x/text v0.3.0: %+v", findings)
	}
	if hit.Kind != "cve" || hit.ProposedType != "security" {
		t.Fatalf("finding = %+v, want kind cve proposing security work", hit)
	}
	switch hit.Severity {
	case "critical", "high", "medium", "low":
	default:
		t.Fatalf("severity %q is not one of the four levels", hit.Severity)
	}
}

// TestScannerFailureIsIsolated makes the CVE scanner's tool fail and asserts
// that RunAll reports exactly that scanner and carries on with the rest.
func TestScannerFailureIsIsolated(t *testing.T) {
	dir := t.TempDir()
	failing := func(ctx context.Context, workdir, name string, args ...string) ([]byte, int, error) {
		if name == "osv-scanner" {
			return nil, 0, fmt.Errorf("osv-scanner: launch failed")
		}
		return analysis.DefaultExec(ctx, workdir, name, args...)
	}

	var failedKinds []string
	maintenance.RunAll(context.Background(), maintenance.ScanInput{WorkDir: dir, Exec: failing},
		func(kind string, err error) { failedKinds = append(failedKinds, kind) })

	if len(failedKinds) != 1 || failedKinds[0] != "cve" {
		t.Fatalf("want exactly the cve scanner to fail, got %v", failedKinds)
	}
}

// TestToolScannerWithoutExecIsAnError pins that a scanner with no way to run
// its tool says so, rather than reporting a clean repository.
func TestToolScannerWithoutExecIsAnError(t *testing.T) {
	for _, kind := range []string{"cve", "outdated_dependency"} {
		if _, err := maintenance.Scanners[kind](context.Background(), maintenance.ScanInput{WorkDir: t.TempDir()}); err == nil {
			t.Errorf("%s scanner with no Exec returned no error", kind)
		}
	}
}

func TestSeverityFromCVSS(t *testing.T) {
	for score, want := range map[float64]string{9.8: "critical", 9.0: "critical", 7.5: "high", 5.3: "medium", 2.0: "low", 0: "high"} {
		if got := maintenance.SeverityFromCVSS(score); got != want {
			t.Errorf("SeverityFromCVSS(%v) = %q, want %q", score, got, want)
		}
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
