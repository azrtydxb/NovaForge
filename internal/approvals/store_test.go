package approvals_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/approvals"
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
	if err := database.Migrate(url, "approvals", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newApprovalsStore(t *testing.T) *approvals.Store {
	t.Helper()
	return approvals.NewStore(storePool(t))
}

func scopedApprovalsCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "agent",
	})
}

func TestRequestResolvePending(t *testing.T) {
	store := newApprovalsStore(t)
	orgID := uuid.New()
	runID := uuid.New()
	ctx := scopedApprovalsCtx(orgID)

	req, err := store.Request(ctx, approvals.ApprovalRequest{
		OrgID:  orgID,
		RunID:  runID,
		Action: approvals.ActionDeployProduction,
		Detail: map[string]any{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.ID == uuid.Nil {
		t.Fatal("want a generated id")
	}
	if req.Decision != "pending" {
		t.Fatalf("want decision pending, got %q", req.Decision)
	}

	pending, err := store.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending request, got %d", len(pending))
	}

	decider := uuid.New()
	if err := store.Resolve(ctx, req.ID, decider, "approved"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	pendingAfter, err := store.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending after resolve: %v", err)
	}
	if len(pendingAfter) != 0 {
		t.Fatalf("want 0 pending requests after resolving, got %d", len(pendingAfter))
	}
}
