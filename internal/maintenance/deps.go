package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// depsReport is the structured JSON procoder's "deps" command prints to
// stdout: packages with a newer version available.
type depsReport struct {
	Outdated []struct {
		Package string `json:"package"`
		Current string `json:"current"`
		Latest  string `json:"latest"`
	} `json:"outdated"`
}

// scanOutdatedDeps detects the outdated_dependency kind by invoking
// procoder's "deps" command. A nil Proc means no way to run it — reported
// as no findings, not an error, so a scan with dependency checking
// unavailable does not stop the other seven scanners from running.
func scanOutdatedDeps(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.Proc == nil {
		return nil, nil
	}
	stdout, _, err := in.Proc(ctx, in.WorkDir, "deps")
	if err != nil {
		return nil, fmt.Errorf("maintenance: outdated dependency scan: procoder deps: %w", err)
	}
	var report depsReport
	if err := json.Unmarshal(stdout, &report); err != nil {
		return nil, fmt.Errorf("maintenance: outdated dependency scan: parse procoder deps output: %w", err)
	}

	findings := make([]Finding, 0, len(report.Outdated))
	for _, dep := range report.Outdated {
		findings = append(findings, Finding{
			Kind:         "outdated_dependency",
			Title:        fmt.Sprintf("%s is outdated (%s -> %s)", dep.Package, dep.Current, dep.Latest),
			Detail:       fmt.Sprintf("current version %s, latest available %s", dep.Current, dep.Latest),
			Severity:     "low",
			Paths:        []string{"go.mod"},
			ProposedType: "upgrade",
		})
	}
	return findings, nil
}

// advisoryReport is the structured JSON procoder's "security" command
// prints to stdout: known vulnerability advisories against the module's
// dependencies. Distinct from gates.securityReport (secrets/SAST findings
// for the security gate): this scanner cares about dependency advisories,
// a different concern procoder's security command also reports.
type advisoryReport struct {
	Advisories []struct {
		Package  string `json:"package"`
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
	} `json:"advisories"`
}

// scanCVE detects the cve kind by invoking procoder's "security" command
// and reading its advisories. A nil Proc means no findings, not an error —
// see scanOutdatedDeps.
func scanCVE(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.Proc == nil {
		return nil, nil
	}
	stdout, _, err := in.Proc(ctx, in.WorkDir, "security")
	if err != nil {
		return nil, fmt.Errorf("maintenance: cve scan: procoder security: %w", err)
	}
	var report advisoryReport
	if err := json.Unmarshal(stdout, &report); err != nil {
		return nil, fmt.Errorf("maintenance: cve scan: parse procoder security output: %w", err)
	}

	findings := make([]Finding, 0, len(report.Advisories))
	for _, a := range report.Advisories {
		findings = append(findings, Finding{
			Kind:         "cve",
			Title:        fmt.Sprintf("%s has a known vulnerability: %s", a.Package, a.Summary),
			Detail:       a.Summary,
			Severity:     normalizeSeverity(a.Severity),
			Paths:        []string{"go.mod"},
			ProposedType: "security",
		})
	}
	return findings, nil
}

// normalizeSeverity lower-cases and validates a severity string against
// the four the platform understands, defaulting to "medium" for anything
// else rather than propagating an unrecognized value.
func normalizeSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical", "high", "medium", "low":
		return strings.ToLower(s)
	default:
		return "medium"
	}
}
