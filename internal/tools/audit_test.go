package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/tools"
)

// TestToolCallAudited is spec S-8: every tool call an agent makes is recorded
// with its arguments and its outcome. It goes through Registry.Call against
// the real audit log, because calling AuditLog directly proves only that the
// log can hold an outcome — not that the registry writes one on each path a
// call can take. The paths asserted: a call that succeeds, a handler that
// fails, a call the capability grant refuses, and calls refused before they
// could run (an unknown tool, and one made with the budget exhausted). A
// refused call is still a call the agent made; leaving it out of the log is
// how a run that tried to write to main would read as one that never did.
func TestToolCallAudited(t *testing.T) {
	pool := auditPool(t)
	audit := agents.NewAuditLog(pool)
	store := agents.NewStore(pool)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newTestRun(t, ctx, store, orgID)

	budget := agents.NewBudget(time.Hour, 1000, 0)
	rt := testRuntime(budget, capability.Grant{WriteBranch: "agents/NF-1/"})
	reg := tools.NewRegistry(rt, audit)
	reg.Register("test.fails", func(ctx context.Context, rt tools.Runtime, argsJSON []byte) ([]byte, error) {
		return nil, fmt.Errorf("the handler could not do it")
	})

	okArgs := `{"work_item_id":"NF-1"}`
	if _, err := reg.Call(ctx, run.ID, "work.get", []byte(okArgs)); err != nil {
		t.Fatalf("work.get: %v", err)
	}
	failArgs := `{"why":"to see the outcome"}`
	if _, err := reg.Call(ctx, run.ID, "test.fails", []byte(failArgs)); err == nil {
		t.Fatal("test.fails: want the handler's error")
	}
	// git.commit to main is outside the grant. The runtime has no git client,
	// so a handler that was reached would fail with a different message.
	deniedArgs := `{"repo":"r","branch":"main","message":"m","files":{"a":"b"}}`
	if _, err := reg.Call(ctx, run.ID, "git.commit", []byte(deniedArgs)); err == nil {
		t.Fatal("git.commit to main: want a capability refusal")
	}
	unknownArgs := `{"cmd":"rm -rf /"}`
	if _, err := reg.Call(ctx, run.ID, "shell.exec", []byte(unknownArgs)); err == nil {
		t.Fatal("shell.exec: want an unknown-tool refusal")
	}
	budget.AddTokens(1001)
	overArgs := `{"work_item_id":"NF-2"}`
	if _, err := reg.Call(ctx, run.ID, "work.get", []byte(overArgs)); err == nil {
		t.Fatal("work.get over budget: want a budget refusal")
	}

	entries, err := audit.List(ctx, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	type want struct {
		tool, args, outcome, errContains string
	}
	wants := []want{
		{"work.get", okArgs, "ok", ""},
		{"test.fails", failArgs, "error", "the handler could not do it"},
		{"git.commit", deniedArgs, "denied", "agents/NF-1/"},
		{"shell.exec", unknownArgs, "refused", "unknown tool"},
		{"work.get", overArgs, "refused", "over budget"},
	}
	if len(entries) != len(wants) {
		t.Fatalf("got %d audit entries, want %d: %+v", len(entries), len(wants), entries)
	}
	for i, w := range wants {
		e := entries[i]
		if e.Tool != w.tool {
			t.Errorf("entry %d tool = %q, want %q", i, e.Tool, w.tool)
		}
		if !jsonEqual(t, e.ArgsJSON, []byte(w.args)) {
			t.Errorf("entry %d (%s) args = %s, want %s", i, w.tool, e.ArgsJSON, w.args)
		}
		if e.Outcome != w.outcome {
			t.Errorf("entry %d (%s) outcome = %q, want %q", i, w.tool, e.Outcome, w.outcome)
		}
		if w.errContains == "" && e.Error != "" {
			t.Errorf("entry %d (%s) error = %q, want none", i, w.tool, e.Error)
		}
		if w.errContains != "" && !strings.Contains(e.Error, w.errContains) {
			t.Errorf("entry %d (%s) error = %q, want it to contain %q", i, w.tool, e.Error, w.errContains)
		}
		if e.EndedAt == nil {
			t.Errorf("entry %d (%s) was never completed: a pending entry says nothing about the outcome", i, w.tool)
		}
	}
}

// TestCancelledCallStillRecordsItsOutcome pins that a call whose run is
// cancelled while its handler runs is completed in the log rather than left
// "pending" forever. The completion was written under the same cancelled
// context and failed, so exactly the calls someone chose to stop were the ones
// with no outcome.
func TestCancelledCallStillRecordsItsOutcome(t *testing.T) {
	pool := auditPool(t)
	audit := agents.NewAuditLog(pool)
	store := agents.NewStore(pool)
	orgID := uuid.New()
	base := scopedCtx(orgID)
	run := newTestRun(t, base, store, orgID)

	ctx, cancel := context.WithCancel(base)
	defer cancel()
	reg := tools.NewRegistry(testRuntime(agents.NewBudget(time.Hour, 1000, 0), capability.Grant{}), audit)
	reg.Register("test.slow", func(ctx context.Context, rt tools.Runtime, argsJSON []byte) ([]byte, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, err := reg.Call(ctx, run.ID, "test.slow", []byte(`{}`)); err == nil {
		t.Fatal("want the cancellation to surface")
	}

	entries, err := audit.List(base, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Outcome != "error" || entries[0].EndedAt == nil {
		t.Fatalf("entries = %+v, want one completed entry with outcome error", entries)
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("bad expected JSON %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}
