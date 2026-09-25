package work_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/work"
)

// A Work Item has to record when it reached done, because nothing else does:
// the row carries created_at and a state, so "how much completed today" cannot
// be derived from it at all. The invariant is maintained by a trigger rather
// than by each writer, because five different statements move an item's state
// and a sixth would silently stop counting.
func TestDoneAtRecordsWhenAnItemCompleted(t *testing.T) {
	pool := storePool(t)
	store := work.NewStore(pool)
	orgID, repoID := uuid.New(), uuid.New()
	ctx := scopedCtx(orgID)

	item, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "finish something"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	doneAt := func() *time.Time {
		t.Helper()
		var at *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT done_at FROM work.work_items WHERE org_id=$1 AND id=$2`, orgID, item.ID).Scan(&at); err != nil {
			t.Fatalf("read done_at: %v", err)
		}
		return at
	}

	if at := doneAt(); at != nil {
		t.Fatalf("a new item already records a completion at %v", at)
	}

	before := time.Now().UTC().Add(-time.Second)
	if err := store.SetState(ctx, item.ID, "done"); err != nil {
		t.Fatalf("SetState done: %v", err)
	}
	at := doneAt()
	if at == nil {
		t.Fatal("an item that reached done records no completion time")
	}
	if at.UTC().Before(before) || at.UTC().After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("completion time %v is not when the item reached done", at)
	}

	// Reopening is not a completion, and the record must not claim otherwise.
	if err := store.SetState(ctx, item.ID, "open"); err != nil {
		t.Fatalf("SetState open: %v", err)
	}
	if at := doneAt(); at != nil {
		t.Fatalf("a reopened item still records a completion at %v", at)
	}

	// Completing again records the second completion, not the first.
	if err := store.SetState(ctx, item.ID, "done"); err != nil {
		t.Fatalf("SetState done again: %v", err)
	}
	again := doneAt()
	if again == nil {
		t.Fatal("recompleting an item records no completion time")
	}
	if !again.After(*at) {
		t.Fatalf("recompletion %v is not after the first completion %v", again, at)
	}
}
