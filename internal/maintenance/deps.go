package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/novaforge/novaforge/internal/analysis"
)

// errNoExec reports a tool-backed scanner run with nothing to run tools with.
// It is an error rather than "no findings": a CVE scan that could not run has
// not found that there are no CVEs, and saying otherwise is exactly the
// silence that hid this scanner never working.
var errNoExec = errors.New("no tool runner configured")

// scanOutdatedDeps detects the outdated_dependency kind from
// `go list -m -u -json all`: direct dependencies with a newer version.
func scanOutdatedDeps(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.Exec == nil {
		return nil, fmt.Errorf("maintenance: outdated dependency scan: %w", errNoExec)
	}
	if !analysis.IsGoModule(in.WorkDir) {
		return nil, nil
	}
	outdated, err := analysis.OutdatedModules(ctx, in.Exec, in.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("maintenance: outdated dependency scan: %w", err)
	}
	findings := make([]Finding, 0, len(outdated))
	for _, dep := range outdated {
		findings = append(findings, Finding{
			Kind:         "outdated_dependency",
			Title:        fmt.Sprintf("%s is outdated (%s -> %s)", dep.Path, dep.Current, dep.Latest),
			Detail:       fmt.Sprintf("current version %s, latest available %s", dep.Current, dep.Latest),
			Severity:     "low",
			Paths:        []string{"go.mod"},
			ProposedType: "upgrade",
		})
	}
	return findings, nil
}

// scanCVE detects the cve kind with osv-scanner, which reads every ecosystem's
// lockfiles, not only Go's.
func scanCVE(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.Exec == nil {
		return nil, fmt.Errorf("maintenance: cve scan: %w", errNoExec)
	}
	vulns, err := analysis.Vulnerabilities(ctx, in.Exec, in.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("maintenance: cve scan: %w", err)
	}
	findings := make([]Finding, 0, len(vulns))
	for _, v := range vulns {
		source := v.Source
		if source == "" {
			source = "go.mod"
		}
		findings = append(findings, Finding{
			Kind:  "cve",
			Title: fmt.Sprintf("%s %s has known vulnerabilities: %s", v.Package, v.Version, strings.Join(v.IDs, ", ")),
			Detail: fmt.Sprintf("%s (%s) — %s. Advisories: %s",
				v.Package, v.Ecosystem, v.Summary, strings.Join(v.IDs, ", ")),
			Severity:     severityFromCVSS(v.MaxSeverity),
			Paths:        []string{source},
			ProposedType: "security",
		})
	}
	return findings, nil
}

// severityFromCVSS maps a CVSS base score onto the platform's four levels
// using the CVSS v3 qualitative bands. An advisory with no score is reported
// as high: it is a known vulnerability, and an unknown severity is a reason
// to look, not a reason to rank it last.
func severityFromCVSS(score float64) string {
	switch {
	case score >= 9.0:
		return "critical"
	case score >= 7.0:
		return "high"
	case score >= 4.0:
		return "medium"
	case score > 0:
		return "low"
	default:
		return "high"
	}
}
