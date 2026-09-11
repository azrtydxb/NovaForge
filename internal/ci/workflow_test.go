package ci_test

import (
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/ci"
)

func TestParseWorkflowWithAgentJob(t *testing.T) {
	data := []byte("jobs:\n  test:\n    run: go test ./...\n  security-review:\n    agent: security\n")
	w, err := ci.ParseWorkflow(data)
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	if w.Jobs["test"].Run != "go test ./..." {
		t.Fatalf("want test job Run %q, got %q", "go test ./...", w.Jobs["test"].Run)
	}
	if w.Jobs["security-review"].Agent != "security" {
		t.Fatalf("want security-review job Agent %q, got %q", "security", w.Jobs["security-review"].Agent)
	}
}

func TestRejectJobWithBothRunAndAgent(t *testing.T) {
	data := []byte("jobs:\n  bad:\n    run: echo hi\n    agent: security\n")
	_, err := ci.ParseWorkflow(data)
	if err == nil {
		t.Fatal("want error for job declaring both run and agent")
	}
	if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("want error containing %q, got %q", "exactly one of", err.Error())
	}
}

func TestTopoSortRespectsNeeds(t *testing.T) {
	w := ci.Workflow{
		Jobs: map[string]ci.Job{
			"a": {Run: "echo a"},
			"b": {Run: "echo b", Needs: []string{"a"}},
		},
	}
	order, err := ci.TopoSort(w)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	idxA, idxB := -1, -1
	for i, name := range order {
		if name == "a" {
			idxA = i
		}
		if name == "b" {
			idxB = i
		}
	}
	if idxA == -1 || idxB == -1 {
		t.Fatalf("want both a and b in order, got %v", order)
	}
	if idxA >= idxB {
		t.Fatalf("want a before b, got order %v", order)
	}
}

func TestTopoSortDetectsCycle(t *testing.T) {
	w := ci.Workflow{
		Jobs: map[string]ci.Job{
			"a": {Run: "echo a", Needs: []string{"b"}},
			"b": {Run: "echo b", Needs: []string{"a"}},
		},
	}
	_, err := ci.TopoSort(w)
	if err == nil {
		t.Fatal("want error for dependency cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want error containing %q, got %q", "cycle", err.Error())
	}
}
