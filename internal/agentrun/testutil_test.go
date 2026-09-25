package agentrun_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/database"
)

func testPool(t *testing.T) *pgxpool.Pool {
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

func newTestAuditLog(t *testing.T) *agents.AuditLog {
	t.Helper()
	return agents.NewAuditLog(testPool(t))
}

func newTestStore(t *testing.T, _ *agents.AuditLog) *agents.Store {
	t.Helper()
	return agents.NewStore(testPool(t))
}

func newAgentRun(t *testing.T, ctx context.Context, store *agents.Store, orgID uuid.UUID) agents.Run {
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
		GrantID:        uuid.Nil, // loop-only fixture issues no capability grant
		Branch:         "agents/NF-1/work",
		WallclockLimit: time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := store.RecordProvenance(ctx, run.ID, agents.Provenance{
		AgentName:   agent.Name,
		ModelName:   agent.ModelRef,
		WorkItemKey: "NF-1",
		RunRef:      "agents/NF-1/work",
		SponsorName: "sponsor",
	}); err != nil {
		t.Fatalf("RecordProvenance: %v", err)
	}
	return run
}
