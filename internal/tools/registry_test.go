package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/tools"
)

// fakeWorkClient is a minimal in-process stand-in for the work service's
// gRPC client, sufficient to exercise the dispatch pipeline without a real
// network call.
type fakeWorkClient struct {
	item tools.WorkItemSummary
}

func (f *fakeWorkClient) Get(ctx context.Context, workItemID string) (tools.WorkItemSummary, error) {
	return f.item, nil
}

func (f *fakeWorkClient) Comment(ctx context.Context, workItemID, body string) error {
	return nil
}

func auditPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "agents", os.DirFS("../agents/migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "agent",
	})
}

func newTestRun(t *testing.T, ctx context.Context, store *agents.Store, orgID uuid.UUID) agents.Run {
	t.Helper()
	agent, err := store.CreateAgent(ctx, agents.Agent{
		OrgID:    orgID,
		Name:     "agent-" + uuid.NewString(),
		Role:     "engineer",
		ModelRef: "stub-model",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	run, err := store.CreateRun(ctx, agents.Run{
		OrgID:          orgID,
		AgentID:        agent.ID,
		SponsorID:      uuid.New(),
		GrantID:        uuid.New(),
		Branch:         "agents/NF-1/work",
		WallclockLimit: time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func testRuntime(budget *agents.Budget, grant capability.Grant) tools.Runtime {
	return tools.Runtime{
		Grant:  grant,
		Budget: budget,
		Work:   &fakeWorkClient{item: tools.WorkItemSummary{ID: "id-1", Key: "NF-1", Goal: "do it", State: "open"}},
	}
}

func TestRegistryHasExactlyThirteenTools(t *testing.T) {
	reg := tools.NewRegistry(tools.Runtime{Budget: agents.NewBudget(time.Hour, 1000, 1000)}, agents.NewAuditLog(nil))
	want := []string{
		"architecture.query",
		"ci.get_logs",
		"ci.run_test",
		"gate.status",
		"git.commit",
		"git.diff",
		"repo.get_dependencies",
		"repo.get_symbol",
		"repo.read_file",
		"repo.search",
		"work.comment",
		"work.get",
		"workspace.read_file",
		"workspace.run",
		"workspace.write_file",
	}
	got := reg.Names()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestUnknownToolRejected(t *testing.T) {
	pool := auditPool(t)
	store := agents.NewStore(pool)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newTestRun(t, ctx, store, orgID)
	reg := tools.NewRegistry(tools.Runtime{Budget: agents.NewBudget(time.Hour, 1000, 1000)}, agents.NewAuditLog(pool))
	_, err := reg.Call(ctx, run.ID, "shell.exec", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if got := err.Error(); !strings.Contains(got, "unknown tool") {
		t.Fatalf("error = %q, want it to contain %q", got, "unknown tool")
	}
}

func TestEveryCallIsAudited(t *testing.T) {
	pool := auditPool(t)
	audit := agents.NewAuditLog(pool)
	store := agents.NewStore(pool)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newTestRun(t, ctx, store, orgID)

	rt := testRuntime(agents.NewBudget(time.Hour, 1000, 1000), capability.Grant{WriteBranch: "agents/NF-1/"})
	reg := tools.NewRegistry(rt, audit)

	args, _ := json.Marshal(map[string]string{"work_item_id": "NF-1"})
	if _, err := reg.Call(ctx, run.ID, "work.get", args); err != nil {
		t.Fatalf("Call: %v", err)
	}

	entries, err := audit.List(ctx, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var matches int
	for _, e := range entries {
		if e.Tool == "work.get" {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one audit entry for work.get, got %d (entries=%v)", matches, entries)
	}
}

func TestBudgetCheckedBeforeCall(t *testing.T) {
	pool := auditPool(t)
	audit := agents.NewAuditLog(pool)
	store := agents.NewStore(pool)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newTestRun(t, ctx, store, orgID)

	budget := agents.NewBudget(time.Hour, 10, 1000)
	budget.AddTokens(11) // exceed the token limit before any call

	handlerCalled := false
	rt := testRuntime(budget, capability.Grant{})
	reg := tools.NewRegistry(rt, audit)
	reg.Register("test.marker", func(ctx context.Context, rt tools.Runtime, argsJSON []byte) ([]byte, error) {
		handlerCalled = true
		return []byte("{}"), nil
	})

	_, err := reg.Call(ctx, run.ID, "test.marker", nil)
	if err == nil {
		t.Fatal("expected error when budget is exhausted")
	}
	if !strings.Contains(err.Error(), "over budget") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "over budget")
	}
	if handlerCalled {
		t.Fatal("handler ran despite exhausted budget")
	}

	// The refused call is on the record as refused: it is still a call the
	// agent made (see TestToolCallAudited), but nothing it asked for ran.
	entries, err := audit.List(ctx, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Tool != "test.marker" || entries[0].Outcome != tools.OutcomeRefused {
		t.Fatalf("entries = %+v, want one refused test.marker entry", entries)
	}
}
