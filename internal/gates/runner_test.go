package gates_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestRunnersCoverEverySevenGates(t *testing.T) {
	want := []string{
		"api-compatibility", "architecture", "dependencies",
		"documentation", "quality", "security", "tests",
	}
	var got []string
	for name := range gates.Runners {
		got = append(got, name)
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("want %d runners, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want runners %v, got %v", want, got)
		}
	}
}

// module writes a Go module into a temp dir. These gate tests run the real
// tools: the previous versions stubbed procoder with JSON procoder never
// prints, and passed while every gate reported "error" on the cluster.
func module(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module example.com/gate\n\ngo 1.22\n"
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func need(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is not installed; the gates run the real tool", tool)
		}
	}
}

func input(dir string, params map[string]any) gates.Input {
	return gates.Input{
		RunID: uuid.New(), TargetSHA: "aaa", WorkDir: dir,
		Params: params, Exec: analysis.DefaultExec,
		SASTRules: os.Getenv("NOVAFORGE_SEMGREP_RULES"),
	}
}

func TestTestsGateFailsBelowCoverage(t *testing.T) {
	need(t, "go")
	dir := module(t, map[string]string{
		"calc.go":      "package gate\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n",
		"calc_test.go": "package gate\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal() } }\n",
	})
	eval, err := gates.Runners["tests"](context.Background(), input(dir, map[string]any{"minimum_coverage": 80.0}))
	if err != nil {
		t.Fatalf("run tests gate: %v", err)
	}
	if eval.Status != "fail" {
		t.Fatalf("want fail at 50%% coverage against an 80%% minimum, got %q: %s", eval.Status, eval.Detail)
	}
	if !strings.Contains(eval.Detail, "50.0") {
		t.Fatalf("want the measured coverage in the detail, got %q", eval.Detail)
	}
}

func TestTestsGatePassesAboveCoverage(t *testing.T) {
	need(t, "go")
	dir := module(t, map[string]string{
		"calc.go":      "package gate\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n",
		"calc_test.go": "package gate\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal() } }\n",
	})
	eval, err := gates.Runners["tests"](context.Background(), input(dir, map[string]any{"minimum_coverage": 80.0}))
	if err != nil {
		t.Fatalf("run tests gate: %v", err)
	}
	if eval.Status != "pass" {
		t.Fatalf("want pass at 100%% coverage, got %q: %s", eval.Status, eval.Detail)
	}
}

func TestSecurityGateFailsOnSecretFinding(t *testing.T) {
	need(t, "gitleaks")
	dir := module(t, map[string]string{
		"config.go": "package gate\n\nconst key = \"" + plantedKey + "\"\n",
	})
	eval, err := gates.Runners["security"](context.Background(), input(dir, nil))
	if err != nil {
		t.Fatalf("run security gate: %v", err)
	}
	if eval.Status != "fail" {
		t.Fatalf("want fail on a planted AWS key, got %q: %s", eval.Status, eval.Detail)
	}
	if strings.Contains(eval.Detail, plantedKey) {
		t.Fatal("the gate detail carried the secret; displaying it is the leak")
	}
}

func TestQualityGateFailsOnUnformattedCode(t *testing.T) {
	need(t, "go", "gofmt")
	dir := module(t, map[string]string{"ugly.go": "package gate\n\n// F is f.\nfunc F()   {  }\n"})
	eval, err := gates.Runners["quality"](context.Background(), input(dir, nil))
	if err != nil {
		t.Fatalf("run quality gate: %v", err)
	}
	if eval.Status != "fail" {
		t.Fatalf("want fail on unformatted code, got %q", eval.Status)
	}
}

func TestDocsGateHonoursLimit(t *testing.T) {
	dir := module(t, map[string]string{"a.go": "package gate\n\nfunc Bare() {}\n"})
	strict, _ := gates.Runners["documentation"](context.Background(), input(dir, nil))
	if strict.Status != "fail" {
		t.Fatalf("want fail with one undocumented export and no allowance, got %q", strict.Status)
	}
	lenient, _ := gates.Runners["documentation"](context.Background(), input(dir, map[string]any{"max_undocumented": 1.0}))
	if lenient.Status != "pass" {
		t.Fatalf("want pass within the allowance, got %q", lenient.Status)
	}
}

// TestGoOnlyGateSkipsANonGoRepository pins that a gate which cannot read a
// repository says so rather than inventing a pass.
func TestGoOnlyGateSkipsANonGoRepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("console.log(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{"tests", "quality", "documentation"} {
		eval, _ := gates.Runners[gate](context.Background(), input(dir, nil))
		if eval.Status != "skipped" {
			t.Errorf("%s on a non-Go repository: want skipped, got %q", gate, eval.Status)
		}
	}
}

// TestToolFailureIsGateError pins that a check which could not run is never a
// pass.
func TestToolFailureIsGateError(t *testing.T) {
	dir := module(t, map[string]string{"a.go": "package gate\n"})
	in := input(dir, nil)
	in.Exec = func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
		return nil, 0, errors.New("launch failed: no such file")
	}
	for _, gate := range []string{"tests", "security", "dependencies", "quality"} {
		eval, err := gates.Runners[gate](context.Background(), in)
		if err != nil {
			t.Fatalf("%s: %v", gate, err)
		}
		if eval.Status != "error" {
			t.Errorf("%s with a tool that could not run: want error, got %q", gate, eval.Status)
		}
	}
}

// plantedKey is a syntactically valid, never-issued AWS access key id for the
// secret scanner to find. It is assembled at run time so the repository's own
// commit gate does not flag this file as leaking a credential.
var plantedKey = "AKIA" + "Z7Q3LXKPWN4TR6HD"
