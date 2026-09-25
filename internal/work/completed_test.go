package work_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/work"
)

// How much work just finished is the platform's own number. It is counted where
// the Work Items live: reviews computes the rest of the dashboard from runs, in
// another service's schema, and a count of completed items cannot be taken from
// there without reading across that boundary.
func TestCompletedSinceCountsOnlyItemsThatAreDoneNow(t *testing.T) {
	pool := storePool(t)
	store := work.NewStore(pool)
	orgID, repoID := uuid.New(), uuid.New()
	ctx := scopedCtx(orgID)

	create := func(goal string) work.Item {
		t.Helper()
		item, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: goal})
		if err != nil {
			t.Fatalf("Create %s: %v", goal, err)
		}
		return item
	}
	midnight := time.Now().UTC().Truncate(24 * time.Hour)

	// Nothing done yet.
	n, err := store.CompletedSince(ctx, orgID, midnight)
	if err != nil {
		t.Fatalf("CompletedSince: %v", err)
	}
	if n != 0 {
		t.Fatalf("an organization with no completed work counts %d", n)
	}

	finished := create("finished today")
	if err := store.SetState(ctx, finished.ID, "done"); err != nil {
		t.Fatalf("SetState done: %v", err)
	}

	// Completed, then reopened: not finished, and must not be counted.
	reopened := create("reopened after finishing")
	if err := store.SetState(ctx, reopened.ID, "done"); err != nil {
		t.Fatalf("SetState done: %v", err)
	}
	if err := store.SetState(ctx, reopened.ID, "open"); err != nil {
		t.Fatalf("SetState open: %v", err)
	}

	// Still in progress.
	create("still going")

	n, err = store.CompletedSince(ctx, orgID, midnight)
	if err != nil {
		t.Fatalf("CompletedSince: %v", err)
	}
	if n != 1 {
		t.Fatalf("counted %d completed items, want 1 (the reopened and unfinished ones must not count)", n)
	}

	// A window that starts after the completion excludes it, so the count is of
	// a period and not of everything that ever finished.
	n, err = store.CompletedSince(ctx, orgID, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("CompletedSince future: %v", err)
	}
	if n != 0 {
		t.Fatalf("a window beginning in the future counts %d completed items", n)
	}
}

// The count is org-scoped like every other query: another organization's
// completed work is not visible in this one's total.
func TestCompletedSinceIsOrganizationScoped(t *testing.T) {
	pool := storePool(t)
	store := work.NewStore(pool)
	mine, theirs := uuid.New(), uuid.New()
	repoID := uuid.New()

	other := scopedCtx(theirs)
	item, err := store.Create(other, work.Item{OrgID: theirs, RepoID: repoID, Type: "feature", Goal: "their work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetState(other, item.ID, "done"); err != nil {
		t.Fatalf("SetState done: %v", err)
	}

	midnight := time.Now().UTC().Truncate(24 * time.Hour)
	n, err := store.CompletedSince(scopedCtx(mine), mine, midnight)
	if err != nil {
		t.Fatalf("CompletedSince: %v", err)
	}
	if n != 0 {
		t.Fatalf("another organization's completed work counted %d in mine", n)
	}
}
