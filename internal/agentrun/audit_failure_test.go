package agentrun_test

import (
	"context"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/tools"
	"testing"
	"time"
)

func TestMissingAuditStopsToolBatch(t *testing.T) {
	audit := testAuditPool(t)
	store := newTestStore(t, audit)
	org := uuid.New()
	ctx := scopedTestCtx(org)
	run := newAgentRun(t, ctx, store, org)
	model := &stubModel{script: []stubResponse{{toolCalls: []provider.ToolCallPart{{ID: "one", Name: "test.marker", Args: []byte(`{}`)}, {ID: "two", Name: "test.marker", Args: []byte(`{}`)}}, totalTokens: 1}, {text: "should not reach", totalTokens: 1}}}
	budget := agents.NewBudget(time.Minute, 100, 0)
	reg := tools.NewRegistry(tools.Runtime{Budget: budget}, nil)
	called := false
	reg.Register("test.marker", func(context.Context, tools.Runtime, []byte) ([]byte, error) { called = true; return nil, nil })
	result, err := agentrun.NewLoop(model, budget, audit).Execute(ctx, run, reg)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "failed" || model.calls != 1 || called {
		t.Fatalf("evidence failure continued execution: state=%s model=%d handler=%v", result.State, model.calls, called)
	}
}
