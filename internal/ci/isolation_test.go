package ci_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/ci"
)

func TestExclusiveCISetupPreservesSharedHistory(t *testing.T) {
	shared := ciPool(t)
	store := ci.NewStore(shared)
	org := uuid.New()
	ctx := scopedCtx(org)
	cleanupOrgRuns(t, shared, org)
	run, _, err := store.CreateRun(ctx, ci.Run{OrgID: org, RepoID: uuid.New(), CommitSHA: uuid.NewString(), Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	isolated := ciPoolExclusive(t)
	var sharedName, isolatedName string
	if err := shared.QueryRow(context.Background(), "SELECT current_database()").Scan(&sharedName); err != nil {
		t.Fatal(err)
	}
	if err := isolated.QueryRow(context.Background(), "SELECT current_database()").Scan(&isolatedName); err != nil {
		t.Fatal(err)
	}
	if sharedName == isolatedName {
		t.Fatal("exclusive CI setup reused the shared test database")
	}
	if _, err := store.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("exclusive CI setup erased shared evidence: %v", err)
	}
	if _, err := ci.NewStore(isolated).GetRun(ctx, run.ID); err == nil {
		t.Fatal("exclusive CI database contains another test's run")
	}
}
