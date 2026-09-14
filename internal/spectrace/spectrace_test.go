// Package spectrace holds TestSpecTraceability, which keeps
// .procoder/specs/traceability.yaml honest against the spec it maps and the
// tests it cites.
//
// The spec names a Go test per acceptance criterion, and most of those names
// were never written. A map from criterion to real evidence is only worth
// something while it stays true: a renamed or deleted test must break it, or
// the map drifts into the same fiction the names became.
package spectrace

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	specPath = ".procoder/specs/backend-platform.md"
	mapPath  = ".procoder/specs/traceability.yaml"
)

type traceMap struct {
	Spec     string               `yaml:"spec"`
	Criteria map[string]criterion `yaml:"criteria"`
}

type criterion struct {
	Status  string     `yaml:"status"`
	Note    string     `yaml:"note"`
	GoTests []string   `yaml:"go_tests"`
	E2E     []e2eEntry `yaml:"e2e"`
}

type e2eEntry struct {
	Script string `yaml:"script"`
	Step   string `yaml:"step"`
}

// criterionLine matches an acceptance-criteria line such as
// "- [ ] [S-6] `TestAgentCIJob`: a CI job ...".
var criterionLine = regexp.MustCompile("^- \\[[ xX]\\] \\[(S-\\d+)\\] `(Test\\w+)`")

func TestSpecTraceability(t *testing.T) {
	root := repoRoot(t)

	want := specCriteria(t, filepath.Join(root, specPath))
	if len(want) == 0 {
		t.Fatalf("%s: found no acceptance criteria — the line format changed, and a map checked against nothing proves nothing", specPath)
	}

	raw, err := os.ReadFile(filepath.Join(root, mapPath))
	if err != nil {
		t.Fatalf("read %s: %v", mapPath, err)
	}
	var m traceMap
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("parse %s: %v", mapPath, err)
	}
	if m.Spec != specPath {
		t.Errorf("%s names spec %q, want %q", mapPath, m.Spec, specPath)
	}

	for id := range want {
		if _, ok := m.Criteria[id]; !ok {
			t.Errorf("criterion %s is in the spec but not in %s", id, mapPath)
		}
	}

	counts := map[string]int{}
	funcs := map[string]map[string]bool{} // package dir -> test functions
	for _, id := range sortedKeys(m.Criteria) {
		c := m.Criteria[id]
		if !want[id] {
			t.Errorf("%s maps %s, which is not an acceptance criterion in the spec", mapPath, id)
		}

		switch c.Status {
		case "covered":
			if len(c.GoTests) == 0 && len(c.E2E) == 0 {
				t.Errorf("%s is marked covered but cites no evidence", id)
			}
		case "partial", "uncovered":
			if strings.TrimSpace(c.Note) == "" {
				t.Errorf("%s is %s but has no note saying what is missing", id, c.Status)
			}
		default:
			t.Errorf("%s has status %q, want covered, partial or uncovered", id, c.Status)
		}
		counts[c.Status]++

		for _, entry := range c.GoTests {
			pkg, name, ok := strings.Cut(strings.TrimSpace(entry), " ")
			if !ok || !strings.HasPrefix(pkg, "./") || !strings.HasPrefix(name, "Test") || strings.Contains(name, " ") {
				t.Errorf("%s: go_tests entry %q is not \"./package/path TestName\"", id, entry)
				continue
			}
			dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(pkg, "./")))
			if _, seen := funcs[dir]; !seen {
				funcs[dir] = testFuncs(t, dir)
			}
			if !funcs[dir][name] {
				t.Errorf("%s: %s cites %s, which does not exist as func %s(t *testing.T) in %s — renamed or deleted?",
					id, mapPath, entry, name, pkg)
			}
		}

		for _, e := range c.E2E {
			script := filepath.Join(root, filepath.FromSlash(e.Script))
			steps, err := scriptSteps(script)
			if err != nil {
				t.Errorf("%s: e2e script %s: %v", id, e.Script, err)
				continue
			}
			if !steps[e.Step] {
				t.Errorf("%s: %s has no step %q (an `echo \"== ... ==\"` line) — renamed or removed?", id, e.Script, e.Step)
			}
		}
	}

	t.Logf("%d criteria: %d covered, %d partial, %d uncovered",
		len(m.Criteria), counts["covered"], counts["partial"], counts["uncovered"])
}

// repoRoot walks up from the test's directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above the test directory")
		}
		dir = parent
	}
}

// specCriteria returns every criterion id ("S-6/TestAgentCIJob") in the
// spec's Acceptance criteria section.
func specCriteria(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open spec: %v", err)
	}
	defer f.Close()

	out := map[string]bool{}
	inSection := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			inSection = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == "Acceptance criteria"
			continue
		}
		if !inSection {
			continue
		}
		if m := criterionLine.FindStringSubmatch(line); m != nil {
			id := m[1] + "/" + m[2]
			if out[id] {
				t.Errorf("spec lists criterion %s twice", id)
			}
			out[id] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read spec: %v", err)
	}
	return out
}

// testFuncs parses every _test.go file in dir and returns the names of the
// top-level functions shaped func TestX(t *testing.T) — what `go test` runs.
func testFuncs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		testingName := importName(parsed, "testing")
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			if takesTestingT(fn, testingName) {
				out[fn.Name.Name] = true
			}
		}
	}
	return out
}

// importName is the name file refers to importPath by, or "" if it does not
// import it.
func importName(file *ast.File, importPath string) string {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return filepath.Base(importPath)
	}
	return ""
}

func takesTestingT(fn *ast.FuncDecl, testingName string) bool {
	if testingName == "" || fn.Type.Results != nil {
		return false
	}
	params := fn.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == testingName
}

var stepLine = regexp.MustCompile(`^\s*echo\s+"== (.+) =="\s*$`)

// scriptSteps returns the step titles an e2e script announces.
func scriptSteps(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("script does not exist: %w", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := stepLine.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out, nil
}

func sortedKeys(m map[string]criterion) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
