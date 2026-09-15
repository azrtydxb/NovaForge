package agentrun_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/tools"
)

// hangingModel answers its script, then behaves like a gateway that never
// answers: it blocks until its context ends. A wall-clock limit that is only
// compared at step boundaries never fires against it — the loop sits inside
// one model call for as long as the model takes.
type hangingModel struct {
	*stubModel
	usage provider.Usage
	hung  bool
}

func (m *hangingModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	if m.calls < len(m.script) {
		resp, err := m.stubModel.Generate(ctx, call)
		if err == nil && m.usage != (provider.Usage{}) {
			resp.Usage = m.usage
		}
		return resp, err
	}
	m.hung = true
	<-ctx.Done()
	return nil, ctx.Err()
}

func callTool(id, tool string) stubResponse {
	return stubResponse{toolCalls: []provider.ToolCallPart{{ID: id, Name: tool, Args: []byte(`{"work_item_id":"NF-1"}`)}}}
}

// TestRunBudgetHardStop is spec S-7: a run exceeding its wall-clock, token or
// cost limit is terminated, marked over budget, and keeps its evidence. Each
// limit is driven through Loop.Execute and then settled through the same
// agents.SettleRun agent-runtime calls, and everything is read back from the
// real store and audit log — the state, which limit stopped it, what it spent,
// and the tool call it made before it was stopped.
func TestRunBudgetHardStop(t *testing.T) {
	// The wall-clock limit leaves room for the first step's round trips to the
	// shared test database, so the run demonstrably reaches the hanging call
	// before the limit passes.
	const wallclock = 3 * time.Second
	var hanging *hangingModel
	cases := []struct {
		name   string
		budget func() *agents.Budget
		model  func() provider.LanguageModel
		price  *agents.TokenPrice
		reason string
	}{
		{
			// The model hangs on its second call; only a deadline on the call
			// itself can stop that.
			name:   "wallclock",
			budget: func() *agents.Budget { return agents.NewBudget(wallclock, 1_000_000, 0) },
			model: func() provider.LanguageModel {
				hanging = &hangingModel{stubModel: &stubModel{script: []stubResponse{callTool("c1", "work.get")}}}
				return hanging
			},
			reason: "wallclock",
		},
		{
			name:   "tokens",
			budget: func() *agents.Budget { return agents.NewBudget(time.Hour, 50, 0) },
			model: func() provider.LanguageModel {
				return &stubModel{script: alwaysCallTool(10, "work.get", 40)}
			},
			reason: "tokens",
		},
		{
			// One micro per input token and two per output token: each step
			// costs 40 + 2*20 = 80 micros against a 100-micro limit, so the
			// second step's cost stops the run before its tool call.
			name:   "cost",
			budget: func() *agents.Budget { return agents.NewBudget(time.Hour, 1_000_000, 100) },
			model: func() provider.LanguageModel {
				return &hangingModel{
					stubModel: &stubModel{script: alwaysCallTool(10, "work.get", 0)},
					usage:     provider.Usage{InputTokens: 40, OutputTokens: 20, TotalTokens: 60},
				}
			},
			price:  &agents.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 2_000_000},
			reason: "cost",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			audit, store, ctx, run := runningRun(t)
			budget := tc.budget()
			reg := tools.NewRegistry(tools.Runtime{
				Budget: budget,
				Grant:  capability.Grant{WriteBranch: "agents/NF-1/"},
				Work:   &fakeWorkClient{},
			}, audit)
			loop := agentrun.NewLoop(tc.model(), budget, audit)
			loop.Runs = store
			loop.Price = tc.price

			type outcome struct {
				res agentrun.Result
				err error
			}
			done := make(chan outcome, 1)
			started := time.Now()
			go func() {
				res, err := loop.Execute(ctx, run, reg)
				done <- outcome{res, err}
			}()
			var got outcome
			select {
			case got = <-done:
			case <-time.After(wallclock + 15*time.Second):
				t.Fatalf("the loop was still running long after its %s limit had been exceeded", tc.name)
			}
			if got.err != nil {
				t.Fatalf("Execute: %v", got.err)
			}
			if got.res.State != "over_budget" {
				t.Fatalf("State = %q (%s), want over_budget", got.res.State, got.res.Summary)
			}
			if tc.name == "wallclock" {
				if !hanging.hung {
					t.Fatal("the run never reached the model call that hangs, so nothing here shows that call being stopped")
				}
				if elapsed := time.Since(started); elapsed > wallclock+5*time.Second {
					t.Fatalf("the wall-clock stop took %s for a %s limit", elapsed, wallclock)
				}
			}

			if err := agents.SettleRun(ctx, store, nil, run, got.res.State); err != nil {
				t.Fatalf("SettleRun: %v", err)
			}

			stored, err := store.GetRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if stored.State != "over_budget" {
				t.Fatalf("stored state = %q, want over_budget", stored.State)
			}
			if stored.EndedAt == nil {
				t.Fatal("an over-budget run has no end time")
			}
			if !strings.Contains(stored.EndReason, tc.reason) {
				t.Fatalf("stored end reason = %q, want it to name the %s limit", stored.EndReason, tc.reason)
			}
			if tc.name == "cost" && stored.CostUsedMicros <= 100 {
				t.Fatalf("stored cost = %d micros, want the spend that exceeded the 100-micro limit", stored.CostUsedMicros)
			}
			if tc.name == "tokens" && stored.TokensUsed <= 50 {
				t.Fatalf("stored tokens = %d, want the spend that exceeded the 50-token limit", stored.TokensUsed)
			}

			// The evidence stays: the tool call made before the stop, and the
			// summary saying how the run ended.
			entries, err := audit.List(ctx, run.ID)
			if err != nil {
				t.Fatalf("audit List: %v", err)
			}
			var sawCall bool
			var summary map[string]string
			for _, e := range entries {
				switch e.Tool {
				case "work.get":
					sawCall = e.Outcome == tools.OutcomeOK
				case "run.summary":
					_ = json.Unmarshal(e.ArgsJSON, &summary)
				}
			}
			if !sawCall {
				t.Fatalf("the tool call made before the stop is not in the audit log: %+v", entries)
			}
			if summary["state"] != "over_budget" {
				t.Fatalf("run.summary = %v, want state over_budget", summary)
			}
		})
	}
}

// TestSettleRunLeavesACancelledRunCancelled pins that settling a run someone
// cancelled does not try to overwrite it.
func TestSettleRunLeavesACancelledRunCancelled(t *testing.T) {
	_, store, ctx, run := runningRun(t)
	if err := store.SetRunState(ctx, run.ID, "cancelled"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := agents.SettleRun(ctx, store, nil, run, "cancelled"); err != nil {
		t.Fatalf("SettleRun: %v", err)
	}
	got, err := store.GetRun(ctx, run.ID)
	if err != nil || got.State != "cancelled" {
		t.Fatalf("state = %q, %v; want cancelled", got.State, err)
	}
}
