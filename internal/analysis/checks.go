package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/cover"
)

// Finding is one thing a check found, in a shape every caller can render.
type Finding struct {
	Rule     string
	Path     string
	Line     int
	Severity string
	Message  string
}

// String renders a finding for a gate's detail or a Work Item's goal.
func (f Finding) String() string {
	loc := f.Path
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	return fmt.Sprintf("%s %s: %s", f.Rule, loc, f.Message)
}

// TestResult is the outcome of running a Go module's tests.
type TestResult struct {
	Passed   bool
	Coverage float64
	// No executable statements means unavailable, not measured zero.
	CoverageAvailable bool
	// Output is the tail of `go test`'s output when tests failed — the part
	// that says which test and why.
	Output string
}

// Tests runs real Go tests and counts covered statements in their profile.
// Display totals round to one decimal and are not suitable history evidence.
func Tests(ctx context.Context, run Exec, dir string) (TestResult, error) {
	profile, err := os.CreateTemp("", "novaforge-cover-*.out")
	if err != nil {
		return TestResult{}, fmt.Errorf("create coverage profile: %w", err)
	}
	profile.Close()
	defer os.Remove(profile.Name())

	out, exit, err := run(ctx, dir, "go", "test", "-coverprofile="+profile.Name(), "./...")
	if err != nil {
		return TestResult{}, err
	}
	if exit != 0 {
		return TestResult{Passed: false, Output: tail(out, 40)}, nil
	}

	info, err := os.Stat(profile.Name())
	if err != nil {
		return TestResult{}, fmt.Errorf("stat coverage evidence: %w", err)
	}
	if info.Size() == 0 {
		return TestResult{}, fmt.Errorf("coverage profile is empty, not a measured zero")
	}
	profiles, err := cover.ParseProfiles(profile.Name())
	if err != nil {
		return TestResult{}, fmt.Errorf("read coverage evidence: %w", err)
	}
	var total, covered int64
	for _, p := range profiles {
		for _, block := range p.Blocks {
			total += int64(block.NumStmt)
			if block.Count > 0 {
				covered += int64(block.NumStmt)
			}
		}
	}
	if total == 0 {
		return TestResult{Passed: true}, nil
	}
	return TestResult{Passed: true, CoverageAvailable: true, Coverage: float64(covered) * 100 / float64(total)}, nil
}

// gitleaksFinding is the subset of gitleaks' JSON report this reads.
type gitleaksFinding struct {
	RuleID      string `json:"RuleID"`
	File        string `json:"File"`
	StartLine   int    `json:"StartLine"`
	Description string `json:"Description"`
}

// Secrets runs gitleaks over the working tree. It scans files rather than
// history (--no-git): a gate judges the change as it would be merged.
// The report goes to a file because gitleaks writes its human summary to
// stdout. The secret value itself is deliberately never carried into a
// Finding: a gate detail is displayed, and displaying the secret would be
// the leak.
func Secrets(ctx context.Context, run Exec, dir string) ([]Finding, error) {
	report, err := os.CreateTemp("", "novaforge-gitleaks-*.json")
	if err != nil {
		return nil, fmt.Errorf("create gitleaks report: %w", err)
	}
	report.Close()
	defer os.Remove(report.Name())

	if _, _, err := run(ctx, dir, "gitleaks", "detect", "--no-git", "--source", dir,
		"--report-format", "json", "--report-path", report.Name(),
		"--exit-code", "0", "--no-banner"); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(report.Name())
	if err != nil {
		return nil, fmt.Errorf("read gitleaks report: %w", err)
	}
	return parseGitleaks(raw, dir)
}

func parseGitleaks(raw []byte, dir string) ([]Finding, error) {
	var found []gitleaksFinding
	if err := json.Unmarshal(raw, &found); err != nil {
		return nil, fmt.Errorf("parse gitleaks report: %w", err)
	}
	out := make([]Finding, 0, len(found))
	for _, f := range found {
		out = append(out, Finding{
			Rule: f.RuleID, Path: relative(dir, f.File), Line: f.StartLine,
			Severity: "critical", Message: f.Description,
		})
	}
	return out, nil
}

// semgrepReport is the subset of semgrep's --json output this reads.
type semgrepReport struct {
	Results []struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line int `json:"line"`
		} `json:"start"`
		Extra struct {
			Message  string `json:"message"`
			Severity string `json:"severity"`
		} `json:"extra"`
	} `json:"results"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// DefaultSASTRules is where the image puts semgrep's gosec ruleset. It is
// fetched at image build time so the check runs with no network: the platform
// is meant to work air-gapped, and a SAST gate that needs the internet to
// evaluate a change does not.
const DefaultSASTRules = "/opt/analysis/semgrep-gosec.yml"

// SAST runs semgrep with a local ruleset. semgrep reads source rather than
// type-checked packages, so it is not broken by a Go release newer than the
// analyzer — which is exactly what disqualified gosec.
func SAST(ctx context.Context, run Exec, dir, rules string) ([]Finding, error) {
	if rules == "" {
		rules = DefaultSASTRules
	}
	if _, err := os.Stat(rules); err != nil {
		return nil, fmt.Errorf("%w: semgrep ruleset %s", ErrToolMissing, rules)
	}
	out, _, err := run(ctx, dir, "semgrep", "--config", rules, "--json", "--quiet",
		"--metrics=off", "--disable-version-check", ".")
	if err != nil {
		return nil, err
	}
	return parseSemgrep(out)
}

func parseSemgrep(out []byte) ([]Finding, error) {
	var report semgrepReport
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, fmt.Errorf("parse semgrep output: %w", err)
	}
	// A rule that failed to run is not a clean result for the code it would
	// have checked.
	if len(report.Errors) > 0 {
		return nil, fmt.Errorf("semgrep reported %d error(s): %s", len(report.Errors), report.Errors[0].Message)
	}
	found := make([]Finding, 0, len(report.Results))
	for _, r := range report.Results {
		rule := r.CheckID
		if i := strings.LastIndex(rule, "."); i >= 0 {
			rule = rule[i+1:]
		}
		found = append(found, Finding{
			Rule: rule, Path: r.Path, Line: r.Start.Line,
			Severity: strings.ToLower(r.Extra.Severity), Message: r.Extra.Message,
		})
	}
	return found, nil
}

// Vulnerability is one known advisory against a dependency.
type Vulnerability struct {
	Package   string
	Version   string
	Ecosystem string
	IDs       []string
	Summary   string
	Source    string
	// MaxSeverity is the highest CVSS base score osv-scanner reports across
	// this package's advisories, or 0 when none carries a score.
	MaxSeverity float64
}

// osvReport is the subset of osv-scanner's --format json output this reads.
type osvReport struct {
	Results []struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Packages []struct {
			Package struct {
				Name      string `json:"name"`
				Version   string `json:"version"`
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
			Vulnerabilities []struct {
				ID      string `json:"id"`
				Summary string `json:"summary"`
			} `json:"vulnerabilities"`
			Groups []struct {
				MaxSeverity string `json:"max_severity"`
			} `json:"groups"`
		} `json:"packages"`
	} `json:"results"`
}

// Vulnerabilities uses only the immutable image's validated offline advisory
// snapshot. Missing, expired or incomplete data is unavailable, never a clean
// dependency gate. Exit 1 means findings; other nonzero exits are scan errors.
func Vulnerabilities(ctx context.Context, run Exec, dir string) ([]Vulnerability, error) {
	return offlineVulnerabilities(ctx, run, dir)
}

func parseOSV(out []byte, dir string) ([]Vulnerability, error) {
	// osv-scanner prints a status line before the JSON on some versions; the
	// report is the first JSON object.
	if i := bytes.IndexByte(out, '{'); i > 0 {
		out = out[i:]
	}
	var report osvReport
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, fmt.Errorf("parse osv-scanner output: %w", err)
	}
	var vulns []Vulnerability
	for _, res := range report.Results {
		for _, p := range res.Packages {
			if len(p.Vulnerabilities) == 0 {
				continue
			}
			v := Vulnerability{
				Package: p.Package.Name, Version: p.Package.Version,
				Ecosystem: p.Package.Ecosystem, Source: relative(dir, res.Source.Path),
				Summary: p.Vulnerabilities[0].Summary,
			}
			for _, adv := range p.Vulnerabilities {
				v.IDs = append(v.IDs, adv.ID)
			}
			for _, g := range p.Groups {
				if score, err := strconv.ParseFloat(g.MaxSeverity, 64); err == nil && score > v.MaxSeverity {
					v.MaxSeverity = score
				}
			}
			vulns = append(vulns, v)
		}
	}
	return vulns, nil
}

// Outdated is a direct dependency with a newer version available.
type Outdated struct {
	Path    string
	Current string
	Latest  string
}

// goModule is the subset of `go list -m -json` output this reads.
type goModule struct {
	Path     string    `json:"Path"`
	Version  string    `json:"Version"`
	Main     bool      `json:"Main"`
	Indirect bool      `json:"Indirect"`
	Update   *goModule `json:"Update"`
}

// OutdatedModules lists the module's direct dependencies that have a newer
// version. `go list -m -u -json all` prints a stream of JSON objects, not an
// array. Indirect dependencies are left out: bumping one is usually not a
// decision a maintainer makes directly, and proposing a Work Item per
// transitive module would bury the ones that are.
func OutdatedModules(ctx context.Context, run Exec, dir string) ([]Outdated, error) {
	out, exit, err := run(ctx, dir, "go", "list", "-m", "-u", "-json", "all")
	if err != nil {
		return nil, err
	}
	if exit != 0 {
		return nil, fmt.Errorf("go list -m -u failed: %s", tail(out, 10))
	}
	return parseGoList(out)
}

func parseGoList(out []byte) ([]Outdated, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var found []Outdated
	for {
		var m goModule
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse go list output: %w", err)
		}
		if m.Main || m.Indirect || m.Update == nil {
			continue
		}
		found = append(found, Outdated{Path: m.Path, Current: m.Version, Latest: m.Update.Version})
	}
	return found, nil
}

// QualityResult is the outcome of the quality check.
type QualityResult struct {
	VetOutput   string
	Unformatted []string
}

// Passed reports whether the quality check found nothing.
func (q QualityResult) Passed() bool { return q.VetOutput == "" && len(q.Unformatted) == 0 }

// Quality runs `go vet` and `gofmt -l`. Both are part of the Go toolchain, so
// they work with whatever Go the module targets.
func Quality(ctx context.Context, run Exec, dir string) (QualityResult, error) {
	var res QualityResult
	vetOut, exit, err := run(ctx, dir, "go", "vet", "./...")
	if err != nil {
		return res, err
	}
	if exit != 0 {
		res.VetOutput = tail(vetOut, 20)
	}
	fmtOut, _, err := run(ctx, dir, "gofmt", "-l", ".")
	if err != nil {
		return res, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(fmtOut)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			res.Unformatted = append(res.Unformatted, line)
		}
	}
	return res, nil
}

func tail(out []byte, lines int) string {
	all := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

func relative(dir, path string) string {
	if rel, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
