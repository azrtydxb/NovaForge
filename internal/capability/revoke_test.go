package capability_test

import (
	"context"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"os"
	"testing"
	"time"
)

func TestRevokeIsScopedAndIdempotent(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(raw, "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := capability.NewStore(pool)
	org, other := uuid.New(), uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "service"})
	grant, err := store.Issue(ctx, capability.Grant{OrgID: org, SubjectID: uuid.New(), SubjectKind: "agent", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(context.Background(), grant.ID); err == nil {
		t.Fatal("anonymous revoke succeeded")
	}
	foreign := authz.WithScope(context.Background(), authz.Scope{OrgID: other, ActorID: uuid.New(), ActorKind: "service"})
	if err := store.Revoke(foreign, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(ctx, grant.ID); err != nil {
		t.Fatal("foreign org revoked grant", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.Revoke(ctx, grant.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Resolve(ctx, grant.ID); err == nil {
		t.Fatal("Resolve still admits revoked grant")
	}
	active, err := store.ListActive(ctx, org, grant.SubjectID)
	if err != nil || len(active) != 0 {
		t.Fatalf("active=%v %v", active, err)
	}
}
