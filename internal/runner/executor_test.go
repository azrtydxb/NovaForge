package runner_test

import (
	"context"
	"testing"
	"time"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/runner"
)

func TestExecuteStreamsLogLines(t *testing.T) {
	job := &civ1.ConnectResponse{RunCmd: "printf 'one\\ntwo\\n'"}
	logs := make(chan string, 10)

	exitCode, err := runner.Execute(context.Background(), job, t.TempDir(), logs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("want exit code 0, got %d", exitCode)
	}

	close(logs)
	var got []string
	for line := range logs {
		got = append(got, line)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("want [\"one\" \"two\"], got %v", got)
	}
}

func TestExecuteReturnsNonZeroExit(t *testing.T) {
	job := &civ1.ConnectResponse{RunCmd: "exit 7"}
	logs := make(chan string, 10)

	exitCode, err := runner.Execute(context.Background(), job, t.TempDir(), logs)
	if err != nil {
		t.Fatalf("want nil error for a failing job (it is a result, not a transport error), got %v", err)
	}
	if exitCode != 7 {
		t.Fatalf("want exit code 7, got %d", exitCode)
	}
}

func TestExecuteRespectsContextCancel(t *testing.T) {
	job := &civ1.ConnectResponse{RunCmd: "sleep 60"}
	logs := make(chan string, 10)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		runner.Execute(ctx, job, t.TempDir(), logs)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("want Execute to return within 5s of context cancellation")
	}
}
