package reviews

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
)

func TestSetRunStateRequiresMatchingOrganization(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "reviews", MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewStore(pool)
	org := uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "user", ActorID: uuid.New()})
	run, err := store.CreateRun(ctx, Run{OrgID: org, RepoID: uuid.New(), Title: "scope", SourceRef: "src", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]context.Context{"foreign": authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New(), ActorKind: "user"}), "absent": context.Background(), "platform": authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "work-reviews"})} {
		t.Run(name, func(t *testing.T) {
			if err := store.setRunState(other, run.ID, "merged"); err == nil {
				t.Error("unscoped state write accepted")
			}
			got, err := store.GetRun(ctx, run.ID)
			if err != nil || got.State != "open" {
				t.Fatalf("foreign write changed state: %s %v", got.State, err)
			}
		})
	}
	if err := store.setRunState(ctx, uuid.New(), "merged"); err == nil {
		t.Error("missing row accepted")
	}
	if err := store.setRunState(ctx, run.ID, "merged"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRun(ctx, run.ID)
	if err != nil || got.State != "merged" {
		t.Fatalf("authorized state update lost: %v %v", got, err)
	}
}
