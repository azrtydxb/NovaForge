package agents_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func storePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "agents", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newStore(t *testing.T) *agents.Store {
	t.Helper()
	return agents.NewStore(storePool(t))
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// actorOf is the person a scoped context acts as: a person may only sponsor a
// run they start themself.
func actorOf(ctx context.Context) string {
	scope, _ := authz.FromContext(ctx)
	return scope.ActorID.String()
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}

func mustCreateAgent(t *testing.T, store *agents.Store, ctx context.Context, orgID uuid.UUID) agents.Agent {
	t.Helper()
	a, err := store.CreateAgent(ctx, agents.Agent{
		OrgID:    orgID,
		Name:     uniqueName("agent"),
		Role:     "reviewer",
		ModelRef: "claude-sonnet-5",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	return a
}

func TestCreateRunRecordsProvenance(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	agent := mustCreateAgent(t, store, ctx, orgID)

	run, err := store.CreateRun(ctx, agents.Run{
		OrgID:           orgID,
		AgentID:         agent.ID,
		SponsorID:       uuid.New(),
		GrantID:         uuid.New(),
		Branch:          "agents/NF-1/run",
		WallclockLimit:  time.Hour,
		TokenLimit:      1_000_000,
		CostLimitMicros: 5_000_000,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	err = store.RecordProvenance(ctx, run.ID, agents.Provenance{
		AgentName:   agent.Name,
		ModelName:   "claude-sonnet-5",
		WorkItemKey: "NF-1",
		RunRef:      "refs/agents/NF-1/run",
		SponsorName: "pascal",
	})
	if err != nil {
		t.Fatalf("RecordProvenance: %v", err)
	}

	got, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Provenance == nil {
		t.Fatal("want provenance, got nil")
	}
	if got.Provenance.AgentName != agent.Name {
		t.Fatalf("want agent name %q, got %q", agent.Name, got.Provenance.AgentName)
	}
	if got.Provenance.ModelName != "claude-sonnet-5" {
		t.Fatalf("want model name claude-sonnet-5, got %q", got.Provenance.ModelName)
	}
	if got.Provenance.SponsorName != "pascal" {
		t.Fatalf("want sponsor name pascal, got %q", got.Provenance.SponsorName)
	}
}

func TestListAgentsIsOrgScoped(t *testing.T) {
	store := newStore(t)
	orgA := uuid.New()
	orgB := uuid.New()

	agentA := mustCreateAgent(t, store, scopedCtx(orgA), orgA)
	agentB := mustCreateAgent(t, store, scopedCtx(orgB), orgB)

	listA, err := store.ListAgents(scopedCtx(orgA))
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	for _, a := range listA {
		if a.ID == agentB.ID {
			t.Fatalf("org A list leaked org B agent %s", agentB.ID)
		}
	}
	found := false
	for _, a := range listA {
		if a.ID == agentA.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("org A list missing its own agent")
	}
}

func TestRunStateTransitions(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)

	run, err := store.CreateRun(ctx, agents.Run{
		OrgID:           orgID,
		AgentID:         agent.ID,
		SponsorID:       uuid.New(),
		GrantID:         uuid.New(),
		Branch:          uniqueName("agents/NF-2/run"),
		WallclockLimit:  time.Hour,
		TokenLimit:      1_000_000,
		CostLimitMicros: 5_000_000,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := store.SetRunState(ctx, run.ID, "running"); err != nil {
		t.Fatalf("queued->running: %v", err)
	}
	if err := store.SetRunState(ctx, run.ID, "succeeded"); err != nil {
		t.Fatalf("running->succeeded: %v", err)
	}
	err = store.SetRunState(ctx, run.ID, "running")
	if err == nil {
		t.Fatal("want error for succeeded->running")
	}
	if !strings.Contains(err.Error(), "invalid transition") {
		t.Fatalf("want error containing %q, got %q", "invalid transition", err.Error())
	}
}
