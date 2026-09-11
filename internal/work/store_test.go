package work_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/work"
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
	if err := database.Migrate(url, "work", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newStore(t *testing.T) *work.Store {
	t.Helper()
	return work.NewStore(storePool(t))
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}

func TestCreateWorkItemAllocatesKey(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	item1, err := store.Create(ctx, work.Item{
		OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "first",
	})
	if err != nil {
		t.Fatalf("Create item1: %v", err)
	}
	item2, err := store.Create(ctx, work.Item{
		OrgID: orgID, RepoID: repoID, Type: "bug", Goal: "second",
	})
	if err != nil {
		t.Fatalf("Create item2: %v", err)
	}

	if item1.Key != "NF-1" {
		t.Fatalf("want key NF-1, got %q", item1.Key)
	}
	if item2.Key != "NF-2" {
		t.Fatalf("want key NF-2, got %q", item2.Key)
	}
}

func TestRejectUnknownType(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	_, err := store.Create(ctx, work.Item{
		OrgID: orgID, RepoID: repoID, Type: "nonsense", Goal: "bad",
	})
	if err == nil {
		t.Fatal("want error for unknown type")
	}
	if !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("want error containing %q, got %q", "invalid type", err.Error())
	}
}

func TestAssignToAgent(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	item, err := store.Create(ctx, work.Item{
		OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "assignable",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	agentID := uuid.New()
	if err := store.Assign(ctx, item.ID, agentID, "agent"); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	got, err := store.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AssigneeID != agentID {
		t.Fatalf("want assignee %v, got %v", agentID, got.AssigneeID)
	}
	if got.AssigneeKind != "agent" {
		t.Fatalf("want assignee kind agent, got %q", got.AssigneeKind)
	}
}

func TestListIsOrgScoped(t *testing.T) {
	store := newStore(t)
	orgA := uuid.New()
	orgB := uuid.New()
	repoID := uuid.New()

	ctxA := scopedCtx(orgA)
	ctxB := scopedCtx(orgB)

	if _, err := store.Create(ctxA, work.Item{OrgID: orgA, RepoID: repoID, Type: "feature", Goal: "a"}); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	if _, err := store.Create(ctxB, work.Item{OrgID: orgB, RepoID: repoID, Type: "feature", Goal: "b"}); err != nil {
		t.Fatalf("Create B: %v", err)
	}

	items, err := store.List(ctxA, orgA, repoID, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, it := range items {
		if it.OrgID != orgA {
			t.Fatalf("List for org A returned item from org %v", it.OrgID)
		}
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item for org A, got %d", len(items))
	}
}
