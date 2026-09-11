package agentrun_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/tools"
)

// stubResponse describes one canned model turn.
type stubResponse struct {
	text        string
	reasoning   string
	toolCalls   []provider.ToolCallPart
	totalTokens int
}

// stubModel is an in-process test double for the go-ai-sdk
// provider.LanguageModel interface: it plays back a fixed script of
// responses rather than making a network call, and records every Call it
// was given.
type stubModel struct {
	script []stubResponse
	calls  int
}

func (m *stubModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	if m.calls >= len(m.script) {
		return nil, fmt.Errorf("stub model script exhausted after %d calls", m.calls)
	}
	r := m.script[m.calls]
	m.calls++

	var content []provider.ContentPart
	if r.reasoning != "" {
		content = append(content, provider.ReasoningPart{Text: r.reasoning})
	}
	if r.text != "" {
		content = append(content, provider.TextPart{Text: r.text})
	}
	finish := provider.FinishStop
	for _, tc := range r.toolCalls {
		content = append(content, tc)
		finish = provider.FinishToolCalls
	}

	return &provider.Response{
		Content:      content,
		FinishReason: finish,
		Usage:        provider.Usage{TotalTokens: r.totalTokens},
	}, nil
}

func (m *stubModel) Stream(ctx context.Context, call provider.Call) (provider.StreamResponse, error) {
	return nil, fmt.Errorf("stub model does not support streaming")
}

func (m *stubModel) ModelID() string      { return "stub-model" }
func (m *stubModel) ProviderName() string { return "stub" }
func (m *stubModel) Capabilities() provider.Capabilities {
	return provider.Capabilities{}
}

// alwaysCallTool builds a script of n identical responses, each requesting
// a call to the named tool, followed by one final response with no tool
// call — used by tests that want the loop to be stopped by something other
// than the model naturally finishing (e.g. the budget).
func alwaysCallTool(n int, tool string, tokensPerStep int) []stubResponse {
	script := make([]stubResponse, 0, n+1)
	for i := 0; i < n; i++ {
		script = append(script, stubResponse{
			toolCalls: []provider.ToolCallPart{
				{ID: fmt.Sprintf("call-%d", i), Name: tool, Args: []byte(`{}`)},
			},
			totalTokens: tokensPerStep,
		})
	}
	script = append(script, stubResponse{text: "done", totalTokens: tokensPerStep})
	return script
}

func testAuditPool(t *testing.T) *agents.AuditLog {
	t.Helper()
	// Reuses the same TEST_DATABASE_URL convention as internal/agents and
	// internal/tools; skips when it is not configured.
	return newTestAuditLog(t)
}

func TestLoopStopsOnBudget(t *testing.T) {
	audit := testAuditPool(t)
	store := newTestStore(t, audit)
	orgID := uuid.New()
	ctx := scopedTestCtx(orgID)
	run := newAgentRun(t, ctx, store, orgID)

	// Each step reports 40 tokens; a 100-token budget allows two full
	// steps (80 tokens) and fails the Check before the third model call.
	budget := agents.NewBudget(time.Hour, 100, 1_000_000)
	model := &stubModel{script: alwaysCallTool(10, "work.get", 40)}

	rt := tools.Runtime{
		Budget: budget,
		Grant:  capability.Grant{WriteBranch: "agents/NF-1/"},
		Work:   &fakeWorkClient{},
	}
	reg := tools.NewRegistry(rt, audit)

	loop := agentrun.NewLoop(model, budget, audit)
	result, err := loop.Execute(ctx, run, reg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.State != "over_budget" {
		t.Fatalf("State = %q, want %q", result.State, "over_budget")
	}

	got, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Provenance == nil {
		t.Fatal("provenance was not recorded before the run went over budget")
	}
}

func TestLoopRecordsEvidenceNotReasoning(t *testing.T) {
	audit := testAuditPool(t)
	store := newTestStore(t, audit)
	orgID := uuid.New()
	ctx := scopedTestCtx(orgID)
	run := newAgentRun(t, ctx, store, orgID)

	const secretReasoning = "SECRET_CHAIN_OF_THOUGHT_MARKER_should_never_be_persisted"

	model := &stubModel{script: []stubResponse{
		{
			reasoning: secretReasoning,
			toolCalls: []provider.ToolCallPart{
				{ID: "call-0", Name: "work.get", Args: []byte(`{}`)},
			},
			totalTokens: 10,
		},
		{text: "finished the work item", totalTokens: 5},
	}}

	budget := agents.NewBudget(time.Hour, 1_000_000, 1_000_000)
	rt := tools.Runtime{
		Budget: budget,
		Grant:  capability.Grant{WriteBranch: "agents/NF-1/"},
		Work:   &fakeWorkClient{},
	}
	reg := tools.NewRegistry(rt, audit)

	loop := agentrun.NewLoop(model, budget, audit)
	result, err := loop.Execute(ctx, run, reg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.State != "succeeded" {
		t.Fatalf("State = %q, want %q", result.State, "succeeded")
	}

	entries, err := audit.List(ctx, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one persisted audit entry")
	}

	var sawToolCall, sawSummary bool
	for _, e := range entries {
		if strings.Contains(string(e.ArgsJSON), secretReasoning) {
			t.Fatalf("audit entry %q leaked model reasoning: %s", e.Tool, e.ArgsJSON)
		}
		if strings.Contains(e.Error, secretReasoning) {
			t.Fatalf("audit entry %q leaked model reasoning in its error field", e.Tool)
		}
		switch e.Tool {
		case "work.get":
			sawToolCall = true
		case "run.summary":
			sawSummary = true
		}
	}
	if !sawToolCall {
		t.Fatal("expected a work.get tool call to be recorded")
	}
	if !sawSummary {
		t.Fatal("expected a final run.summary entry to be recorded")
	}
}

func TestLoopSurfacesToolErrors(t *testing.T) {
	audit := testAuditPool(t)
	store := newTestStore(t, audit)
	orgID := uuid.New()
	ctx := scopedTestCtx(orgID)
	run := newAgentRun(t, ctx, store, orgID)

	model := &stubModel{script: []stubResponse{
		{
			toolCalls: []provider.ToolCallPart{
				{ID: "call-0", Name: "work.comment", Args: []byte(`{}`)},
			},
			totalTokens: 5,
		},
		{text: "handled the error", totalTokens: 5},
	}}

	budget := agents.NewBudget(time.Hour, 1_000_000, 1_000_000)
	rt := tools.Runtime{
		Budget: budget,
		Grant:  capability.Grant{WriteBranch: "agents/NF-1/"},
		Work:   &failingWorkClient{},
	}
	reg := tools.NewRegistry(rt, audit)

	loop := agentrun.NewLoop(model, budget, audit)
	result, err := loop.Execute(ctx, run, reg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.State != "succeeded" {
		t.Fatalf("State = %q, want %q (a tool error should be fed back to the model, not abort the run)", result.State, "succeeded")
	}
	if model.calls != 2 {
		t.Fatalf("model.calls = %d, want 2 (the loop must call the model again after the failing tool call)", model.calls)
	}
}

type fakeWorkClient struct{}

func (f *fakeWorkClient) Get(ctx context.Context, workItemID string) (tools.WorkItemSummary, error) {
	return tools.WorkItemSummary{ID: workItemID, Key: "NF-1", Goal: "test", State: "open"}, nil
}
func (f *fakeWorkClient) Comment(ctx context.Context, workItemID, body string) error { return nil }

type failingWorkClient struct{}

func (f *failingWorkClient) Get(ctx context.Context, workItemID string) (tools.WorkItemSummary, error) {
	return tools.WorkItemSummary{}, nil
}
func (f *failingWorkClient) Comment(ctx context.Context, workItemID, body string) error {
	return fmt.Errorf("comment rejected: work item locked")
}

func scopedTestCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "agent",
	})
}
