package maintenance_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/maintenance"
	"github.com/novaforge/novaforge/internal/work"
)

func proposeDBURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func proposeWorkStore(t *testing.T) *work.Store {
	t.Helper()
	url := proposeDBURL(t)
	if err := database.Migrate(url, "work", os.DirFS("../work/migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return work.NewStore(pool)
}

func proposeScopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}

func cveFinding() maintenance.Finding {
	return maintenance.Finding{
		Kind:         "cve",
		Title:        "github.com/example/vuln has a known vulnerability",
		Detail:       "CVE-2026-0001",
		Severity:     "critical",
		Paths:        []string{"go.mod"},
		ProposedType: "security",
	}
}

func TestProposalCreatesUnassignedWorkItem(t *testing.T) {
	store := proposeWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := proposeScopedCtx(orgID)

	p := &maintenance.Proposer{Work: store}
	items, err := p.Propose(ctx, orgID, repoID, []maintenance.Finding{cveFinding()})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 proposed work item, got %d", len(items))
	}
	item := items[0]
	if item.Type != "security" {
		t.Fatalf("want type security, got %q", item.Type)
	}
	if item.State != "open" {
		t.Fatalf("want state open, got %q", item.State)
	}
	if item.AssigneeKind != "" || item.AssigneeID != uuid.Nil {
		t.Fatalf("want no assignee, got kind %q id %v", item.AssigneeKind, item.AssigneeID)
	}
}

func TestDuplicateFindingDoesNotDuplicateWorkItem(t *testing.T) {
	store := proposeWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := proposeScopedCtx(orgID)

	p := &maintenance.Proposer{Work: store}
	finding := cveFinding()

	if _, err := p.Propose(ctx, orgID, repoID, []maintenance.Finding{finding}); err != nil {
		t.Fatalf("Propose (1st): %v", err)
	}
	items, err := p.Propose(ctx, orgID, repoID, []maintenance.Finding{finding})
	if err != nil {
		t.Fatalf("Propose (2nd): %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item on the 2nd proposal of the identical finding, got %d", len(items))
	}

	all, err := store.List(ctx, orgID, repoID, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want exactly 1 work item to exist, got %d", len(all))
	}
}

func TestResolvedFindingClosesProposal(t *testing.T) {
	store := proposeWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := proposeScopedCtx(orgID)

	p := &maintenance.Proposer{Work: store}
	finding := cveFinding()

	items, err := p.Propose(ctx, orgID, repoID, []maintenance.Finding{finding})
	if err != nil {
		t.Fatalf("Propose (1st): %v", err)
	}
	itemID := items[0].ID

	// Rescan without the finding: it no longer reproduces.
	if _, err := p.Propose(ctx, orgID, repoID, nil); err != nil {
		t.Fatalf("Propose (2nd, empty): %v", err)
	}

	got, err := store.Get(ctx, itemID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != "done" {
		t.Fatalf("want the stale proposal moved to done, got state %q", got.State)
	}

	all, err := store.List(ctx, orgID, repoID, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want the work item to still exist (closed, not deleted), got %d items", len(all))
	}
}

func TestProposalNeverStartsAnAgentRun(t *testing.T) {
	store := proposeWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := proposeScopedCtx(orgID)

	p := &maintenance.Proposer{Work: store}
	if _, err := p.Propose(ctx, orgID, repoID, []maintenance.Finding{cveFinding()}); err != nil {
		t.Fatalf("Propose: %v", err)
	}

	url := proposeDBURL(t)
	if err := database.Migrate(url, "agents", os.DirFS("../agents/migrations")); err != nil {
		t.Fatalf("Migrate agents: %v", err)
	}
	var pool *pgxpool.Pool
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	agentsStore := agents.NewStore(pool)

	// Scoped to this test's own random orgID: the agents.agent_runs table
	// is a shared, persistent test database, so counting every row in it
	// would pick up unrelated rows from other tests run against the same
	// Postgres instance.
	var count int
	if err := agentsStore.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM agents.agent_runs WHERE org_id = $1`, orgID,
	).Scan(&count); err != nil {
		t.Fatalf("count agent_runs: %v", err)
	}
	if count != 0 {
		t.Fatalf("want no Agent Runs created by Propose for org %s, found %d", orgID, count)
	}
}
