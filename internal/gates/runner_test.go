package gates_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

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

func TestTestsGateFailsBelowCoverage(t *testing.T) {
	in := gates.Input{
		RunID:     uuid.New(),
		TargetSHA: "aaa",
		Params:    map[string]any{"minimum_coverage": 80.0},
		Proc: func(ctx context.Context, workdir string, args ...string) ([]byte, int, error) {
			body, _ := json.Marshal(map[string]any{"coverage": 61.0})
			return body, 0, nil
		},
	}

	eval, err := gates.Runners["tests"](context.Background(), in)
	if err != nil {
		t.Fatalf("run tests gate: %v", err)
	}
	if eval.Status != "fail" {
		t.Fatalf("want status fail, got %q", eval.Status)
	}
	if !strings.Contains(eval.Detail, "61") {
		t.Fatalf("want detail to contain 61, got %q", eval.Detail)
	}
}

func TestSecurityGateFailsOnSecretFinding(t *testing.T) {
	in := gates.Input{
		RunID:     uuid.New(),
		TargetSHA: "aaa",
		Proc: func(ctx context.Context, workdir string, args ...string) ([]byte, int, error) {
			body, _ := json.Marshal(map[string]any{"secrets": []string{"AWS_KEY at config.go:12"}})
			return body, 1, nil
		},
	}

	eval, err := gates.Runners["security"](context.Background(), in)
	if err != nil {
		t.Fatalf("run security gate: %v", err)
	}
	if eval.Status != "fail" {
		t.Fatalf("want status fail, got %q", eval.Status)
	}
}

func TestProcoderErrorIsGateError(t *testing.T) {
	in := gates.Input{
		RunID:     uuid.New(),
		TargetSHA: "aaa",
		Proc: func(ctx context.Context, workdir string, args ...string) ([]byte, int, error) {
			return nil, 0, errors.New("transport error: connection refused")
		},
	}

	eval, err := gates.Runners["tests"](context.Background(), in)
	if err != nil {
		t.Fatalf("run tests gate: %v", err)
	}
	if eval.Status != "error" {
		t.Fatalf("want status error, got %q", eval.Status)
	}
	if eval.Status == "pass" {
		t.Fatal("a procoder transport error must never be reported as pass")
	}
}
