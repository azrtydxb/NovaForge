package gates_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/gates"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// archFixture builds a tiny real Go module in a temp dir with two packages,
// frontend and database, where frontend imports database — so the
// architecture gate has a real import graph to walk with go list. These
// component tests explicitly execute only this test-authored source locally;
// they do not qualify sandbox isolation or add a production fallback.
func archFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module archfixture\n\ngo 1.26\n")
	mustWriteFile(t, filepath.Join(dir, "database", "database.go"),
		"package database\n\nfunc Query() string { return \"ok\" }\n")
	mustWriteFile(t, filepath.Join(dir, "frontend", "frontend.go"),
		"package frontend\n\nimport \"archfixture/database\"\n\nfunc Render() string { return database.Query() }\n")
	return dir
}

func TestForbiddenDependencyDetected(t *testing.T) {
	dir := archFixture(t)
	in := gates.Input{
		RunID:     uuid.New(),
		WorkDir:   dir,
		TargetSHA: "aaa",
		Params:    map[string]any{"forbidden_dependencies": []any{"frontend -> database"}},
		Exec:      analysis.DefaultExec,
	}

	eval, err := gates.Runners["architecture"](context.Background(), in)
	if err != nil {
		t.Fatalf("run architecture gate: %v", err)
	}
	if eval.Status != "fail" {
		t.Fatalf("want status fail, got %q (detail: %s)", eval.Status, eval.Detail)
	}
	if !strings.Contains(eval.Detail, "frontend") || !strings.Contains(eval.Detail, "database") {
		t.Fatalf("want detail naming both packages, got %q", eval.Detail)
	}
}

func TestAllowedDependencyPasses(t *testing.T) {
	dir := archFixture(t)
	in := gates.Input{
		RunID:     uuid.New(),
		WorkDir:   dir,
		TargetSHA: "aaa",
		Params:    map[string]any{"forbidden_dependencies": []any{"database -> frontend"}},
		Exec:      analysis.DefaultExec,
	}

	eval, err := gates.Runners["architecture"](context.Background(), in)
	if err != nil {
		t.Fatalf("run architecture gate: %v", err)
	}
	if eval.Status != "pass" {
		t.Fatalf("want status pass, got %q (detail: %s)", eval.Status, eval.Detail)
	}
}
