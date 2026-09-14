package analysis_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/analysis"
)

// These tests run the real tools. They exist because the integration this
// package replaces was tested only against stubs that returned the JSON the
// code expected — and the tool never printed that JSON. A stub of an external
// tool proves the parser matches the stub, not the tool.
//
// A missing tool fails the test rather than skipping it: a skipped check reads
// as a pass, and "the secret scanner could not run" is not "no secrets".

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s is not installed; these tests run the real tool (see deploy/docker/Dockerfile.analysis)", name)
	}
}

// probe writes a small Go module into a temp dir.
func probe(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const goMod = "module example.com/probe\n\ngo 1.22\n"

func TestTestsReportsCoverage(t *testing.T) {
	requireTool(t, "go")
	dir := probe(t, map[string]string{
		"go.mod":       goMod,
		"calc.go":      "package probe\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n",
		"calc_test.go": "package probe\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal() } }\n",
	})
	res, err := analysis.Tests(context.Background(), analysis.DefaultExec, dir)
	if err != nil {
		t.Fatalf("Tests: %v", err)
	}
	if !res.Passed {
		t.Fatalf("tests should pass: %s", res.Output)
	}
	// One of two functions is exercised.
	if res.Coverage < 40 || res.Coverage > 60 {
		t.Fatalf("coverage = %.1f%%, want about 50%%", res.Coverage)
	}
}

func TestTestsReportsAFailure(t *testing.T) {
	requireTool(t, "go")
	dir := probe(t, map[string]string{
		"go.mod":       goMod,
		"calc.go":      "package probe\n\n// Add adds.\nfunc Add(a, b int) int { return a - b }\n",
		"calc_test.go": "package probe\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"wrong sum\") } }\n",
	})
	res, err := analysis.Tests(context.Background(), analysis.DefaultExec, dir)
	if err != nil {
		t.Fatalf("Tests: %v", err)
	}
	if res.Passed {
		t.Fatal("a failing test must not report as passed")
	}
	if !strings.Contains(res.Output, "TestAdd") {
		t.Fatalf("the failure output should name the test: %q", res.Output)
	}
}

func TestSecretsFindsAPlantedKey(t *testing.T) {
	requireTool(t, "gitleaks")
	// Not the AWS documentation example keys: gitleaks allowlists those.
	dir := probe(t, map[string]string{
		"config.go": "package probe\n\nconst key = \"" + plantedKey + "\"\n",
	})
	found, err := analysis.Secrets(context.Background(), analysis.DefaultExec, dir)
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("gitleaks found nothing in a file containing an AWS access key")
	}
	for _, f := range found {
		if strings.Contains(f.String(), plantedKey) {
			t.Fatal("a finding carried the secret itself; displaying it would be the leak")
		}
	}
}

func TestSecretsCleanTree(t *testing.T) {
	requireTool(t, "gitleaks")
	dir := probe(t, map[string]string{"main.go": "package probe\n"})
	found, err := analysis.Secrets(context.Background(), analysis.DefaultExec, dir)
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("clean tree reported %d secrets: %v", len(found), found)
	}
}

func TestSASTFindsWeakCrypto(t *testing.T) {
	requireTool(t, "semgrep")
	rules := os.Getenv("NOVAFORGE_SEMGREP_RULES")
	if rules == "" {
		t.Fatal("NOVAFORGE_SEMGREP_RULES must point at the gosec ruleset (hack/fetch-analysis-rules.sh fetches it)")
	}
	dir := probe(t, map[string]string{
		"go.mod":  goMod,
		"hash.go": "package probe\n\nimport \"crypto/md5\"\n\n// Sum hashes.\nfunc Sum(b []byte) [16]byte { return md5.Sum(b) }\n",
	})
	found, err := analysis.SAST(context.Background(), analysis.DefaultExec, dir, rules)
	if err != nil {
		t.Fatalf("SAST: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("semgrep found nothing in a file using md5")
	}
}

func TestSASTWithoutRulesIsAnError(t *testing.T) {
	_, err := analysis.SAST(context.Background(), analysis.DefaultExec, t.TempDir(), "/nonexistent/rules.yml")
	if !errors.Is(err, analysis.ErrToolMissing) {
		t.Fatalf("a SAST check with no ruleset must fail loudly, got %v", err)
	}
}

func TestVulnerabilitiesFindsAKnownCVE(t *testing.T) {
	requireTool(t, "osv-scanner")
	requireTool(t, "go")
	// golang.org/x/text v0.3.0 carries several published advisories.
	dir := probe(t, map[string]string{
		"go.mod":  "module example.com/probe\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.0\n",
		"main.go": "package probe\n\nimport _ \"golang.org/x/text/language\"\n",
	})
	if out, err := exec.Command("go", "-C", dir, "mod", "download", "golang.org/x/text").CombinedOutput(); err != nil {
		t.Fatalf("download module: %v: %s", err, out)
	}
	if out, err := exec.Command("go", "-C", dir, "mod", "tidy").CombinedOutput(); err != nil {
		t.Fatalf("tidy: %v: %s", err, out)
	}
	vulns, err := analysis.Vulnerabilities(context.Background(), analysis.DefaultExec, dir)
	if err != nil {
		t.Fatalf("Vulnerabilities: %v", err)
	}
	var hit bool
	for _, v := range vulns {
		if v.Package == "golang.org/x/text" && len(v.IDs) > 0 {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("osv-scanner did not report golang.org/x/text v0.3.0: %+v", vulns)
	}
}

func TestOutdatedModulesFindsANewerVersion(t *testing.T) {
	requireTool(t, "go")
	dir := probe(t, map[string]string{
		"go.mod":  "module example.com/probe\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.0\n",
		"main.go": "package probe\n\nimport _ \"golang.org/x/text/language\"\n",
	})
	if out, err := exec.Command("go", "-C", dir, "mod", "tidy").CombinedOutput(); err != nil {
		t.Fatalf("tidy: %v: %s", err, out)
	}
	found, err := analysis.OutdatedModules(context.Background(), analysis.DefaultExec, dir)
	if err != nil {
		t.Fatalf("OutdatedModules: %v", err)
	}
	for _, o := range found {
		if o.Path == "golang.org/x/text" && o.Latest != "" && o.Latest != o.Current {
			return
		}
	}
	t.Fatalf("golang.org/x/text v0.3.0 was not reported as outdated: %+v", found)
}

func TestQualityFindsUnformattedCode(t *testing.T) {
	requireTool(t, "go")
	requireTool(t, "gofmt")
	dir := probe(t, map[string]string{
		"go.mod":  goMod,
		"ugly.go": "package probe\n\n// F is f.\nfunc F()   {  }\n",
	})
	res, err := analysis.Quality(context.Background(), analysis.DefaultExec, dir)
	if err != nil {
		t.Fatalf("Quality: %v", err)
	}
	if res.Passed() || len(res.Unformatted) == 0 {
		t.Fatalf("an unformatted file was not reported: %+v", res)
	}
}

func TestUndocumentedExports(t *testing.T) {
	dir := probe(t, map[string]string{
		"a.go": "package probe\n\n// Documented has a comment.\nfunc Documented() {}\n\nfunc Bare() {}\n\ntype hidden struct{}\n\nfunc (hidden) Method() {}\n",
	})
	found, err := analysis.UndocumentedExports(dir)
	if err != nil {
		t.Fatalf("UndocumentedExports: %v", err)
	}
	if len(found) != 1 || !strings.Contains(found[0].Message, "Bare") {
		t.Fatalf("want exactly Bare reported, got %+v", found)
	}
}

func TestMissingToolIsAnError(t *testing.T) {
	_, _, err := analysis.DefaultExec(context.Background(), t.TempDir(), "definitely-not-a-real-tool-xyz")
	if !errors.Is(err, analysis.ErrToolMissing) {
		t.Fatalf("a missing tool must be ErrToolMissing, got %v", err)
	}
}

// plantedKey is a syntactically valid, never-issued AWS access key id for the
// secret scanner to find. It is assembled at run time so the repository's own
// commit gate does not flag this file as leaking a credential.
var plantedKey = "AKIA" + "Z7Q3LXKPWN4TR6HD"
