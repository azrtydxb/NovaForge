package agentrun_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/tools"
)

func TestFinalResponseCannotBypassBudget(t *testing.T) {
	for _, verification := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion", true: "verification"}[verification], func(t *testing.T) {
			audit, store, ctx, run := runningRun(t)
			model := &stubModel{script: []stubResponse{{text: "done", totalTokens: 60}}}
			if verification {
				model.script = []stubResponse{{text: "done", totalTokens: 1}, {text: `{"verdicts":[{"met":true,"evidence":"step 1"}]}`, totalTokens: 60}}
			}
			budget := agents.NewBudget(time.Minute, 50, 0)
			loop := agentrun.NewLoop(model, budget, audit)
			loop.Runs = store
			if verification {
				loop.Criteria = func(context.Context, uuid.UUID) (agentrun.Criteria, error) {
					return agentrun.Criteria{Acceptance: []string{"done"}}, nil
				}
			}
			result, err := loop.Execute(ctx, run, tools.NewRegistry(tools.Runtime{Budget: budget}, audit))
			if err != nil || result.State != "over_budget" {
				t.Fatalf("result = %+v, %v; want over_budget", result, err)
			}
			stored, err := store.GetRun(ctx, run.ID)
			if err != nil || stored.TokensUsed <= 50 {
				t.Fatalf("spend not retained: %+v, %v", stored, err)
			}
		})
	}
}

// The database write represents CancelRun on a different replica while a
// model request is in flight; no local cancel function is invoked.
func TestRemoteCancellationInterruptsModel(t *testing.T) {
	audit, store, ctx, run := runningRun(t)
	started := make(chan struct{})
	model := &blockingModel{started: started}
	budget := agents.NewBudget(time.Minute, 1000, 0)
	loop := agentrun.NewLoop(model, budget, audit)
	loop.Runs = store
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan agentrun.Result, 1)
	go func() {
		res, _ := loop.Execute(runCtx, run, tools.NewRegistry(tools.Runtime{Budget: budget}, audit))
		done <- res
	}()
	<-started
	if err := store.SetRunState(ctx, run.ID, "cancelled"); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res.State != "cancelled" {
			t.Fatalf("state=%s", res.State)
		}
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("remote cancellation did not interrupt model call")
	}
}

type blockingModel struct {
	stubModel
	started chan struct{}
}

func (m *blockingModel) Generate(ctx context.Context, _ provider.Call) (*provider.Response, error) {
	close(m.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestModelUsageMustBeObservable(t *testing.T) {
	for _, usage := range []provider.Usage{{}, {TotalTokens: -1}, {TotalTokens: 10}} {
		audit, store, ctx, run := runningRun(t)
		model := &usageModel{usage: usage}
		budget := agents.NewBudget(time.Minute, 1000, 1000)
		loop := agentrun.NewLoop(model, budget, audit)
		loop.Runs = store
		loop.Price = &agents.TokenPrice{InputMicrosPerMillion: 1, OutputMicrosPerMillion: 1}
		result, err := loop.Execute(ctx, run, tools.NewRegistry(tools.Runtime{Budget: budget}, audit))
		if err != nil || result.State != "failed" {
			t.Fatalf("usage %+v accepted: %+v %v", usage, result, err)
		}
	}
}

type usageModel struct {
	stubModel
	usage provider.Usage
}

func (m *usageModel) Generate(context.Context, provider.Call) (*provider.Response, error) {
	return &provider.Response{Content: []provider.ContentPart{provider.TextPart{Text: "done"}}, Usage: m.usage}, nil
}

func TestSettleContextCancellationPersistsTerminalIntent(t *testing.T) {
	_, store, ctx, run := runningRun(t)
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := agents.SettleRun(cancelled, store, nil, run, "cancelled"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetRun(ctx, run.ID)
	if err != nil || saved.State != "cancelled" {
		t.Fatalf("terminal cancellation missing: %+v %v", saved, err)
	}
}

func TestSingleResponseOverflowFailsAtMaximumLimits(t *testing.T) {
	for i, usage := range []provider.Usage{{InputTokens: 1_000_000, OutputTokens: 1_000_000}, {InputTokens: math.MaxInt64, OutputTokens: 1}} {
		budget := agents.NewBudget(time.Minute, math.MaxInt64, math.MaxInt64)
		loop := agentrun.NewLoop(&usageModel{usage: usage}, budget, nil)
		if i == 0 {
			loop.Price = &agents.TokenPrice{InputMicrosPerMillion: 1 << 62, OutputMicrosPerMillion: 1 << 62}
		}
		result, err := loop.Execute(context.Background(), agents.Run{}, tools.NewRegistry(tools.Runtime{Budget: budget}, nil))
		if err != nil || result.State != "over_budget" {
			t.Fatalf("single response overflow accepted: %+v %v", result, err)
		}
	}
}

func TestFinalAccountingLockTimeoutIsNotSilentSuccess(t *testing.T) {
	audit, store, ctx, run := runningRun(t)
	tx, err := store.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM agents.agent_runs WHERE id=$1 FOR UPDATE`, run.ID); err != nil {
		t.Fatal(err)
	}
	budget := agents.NewBudget(time.Minute, 1000, 0)
	loop := agentrun.NewLoop(&stubModel{script: []stubResponse{{text: "done", totalTokens: 73}}}, budget, audit)
	loop.Runs = store
	result, err := loop.Execute(ctx, run, tools.NewRegistry(tools.Runtime{Budget: budget}, audit))
	if err == nil {
		t.Errorf("lost final persistence returned implicit success: %+v", result)
	}
	if result.TokensUsed != 73 {
		t.Errorf("retry lost measured result: %+v", result)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.GetRun(ctx, run.ID)
	if saved.State == "succeeded" && saved.TokensUsed == 0 {
		t.Fatal("terminal measured-zero success")
	}
}

func TestLocalCancellationWhileRowRunning(t *testing.T) {
	audit, store, ctx, run := runningRun(t)
	model := &blockingModel{started: make(chan struct{})}
	budget := agents.NewBudget(time.Minute, 1000, 0)
	loop := agentrun.NewLoop(model, budget, audit)
	loop.Runs = store
	local, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan agentrun.Result, 1)
	go func() {
		res, _ := loop.Execute(local, run, tools.NewRegistry(tools.Runtime{Budget: budget}, audit))
		done <- res
	}()
	<-model.started
	cancel()
	res := <-done
	if res.State != "cancelled" {
		t.Fatalf("local cancellation classified %s", res.State)
	}
	saved, _ := store.GetRun(ctx, run.ID)
	if saved.State != "cancelled" {
		t.Fatalf("state not durable: %s", saved.State)
	}
}

func TestMalformedVerificationRetainsMeasuredUsage(t *testing.T) {
	audit, store, ctx, run := runningRun(t)
	budget := agents.NewBudget(time.Minute, 1000, 0)
	loop := agentrun.NewLoop(&stubModel{script: []stubResponse{{text: "done", totalTokens: 7}, {text: "not json", totalTokens: 19}}}, budget, audit)
	loop.Runs = store
	loop.Criteria = func(context.Context, uuid.UUID) (agentrun.Criteria, error) {
		return agentrun.Criteria{Acceptance: []string{"done"}}, nil
	}
	result, _ := loop.Execute(ctx, run, tools.NewRegistry(tools.Runtime{Budget: budget}, audit))
	if result.State != "failed" || result.TokensUsed != 26 || !result.TokensAvailable || result.CostAvailable {
		t.Fatalf("verifier spend lost: %+v", result)
	}
}

func TestStdioClosureStopsRemainingToolBatch(t *testing.T) {
	audit, store, ctx, run := runningRun(t)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	budget := agents.NewBudget(time.Minute, 1000, 0)
	model := &stubModel{script: []stubResponse{{toolCalls: []provider.ToolCallPart{{ID: "close", Name: "close", Args: []byte(`{}`)}, {ID: "later", Name: "later", Args: []byte(`{}`)}}, totalTokens: 1}}}
	loop := agentrun.NewLoop(model, budget, audit)
	loop.Runs = store
	reg := tools.NewRegistry(tools.Runtime{Budget: budget}, audit)
	reg.Register("close", func(context.Context, tools.Runtime, []byte) ([]byte, error) {
		cancel(tools.ErrMCPStdioClosed)
		return nil, tools.ErrMCPStdioClosed
	})
	later := false
	reg.Register("later", func(context.Context, tools.Runtime, []byte) ([]byte, error) { later = true; return []byte(`{}`), nil })
	result, err := loop.Execute(ctx, run, reg)
	if err != nil || later || result.State != "failed" {
		t.Fatalf("work after workspace invalidation: later=%v %+v %v", later, result, err)
	}
}
