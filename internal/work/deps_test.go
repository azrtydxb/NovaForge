package work_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/work"
)

func TestReadyExcludesBlockedItems(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "epic"})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}
	a, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "A"})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	b, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "B"})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if err := store.SetParent(ctx, a.ID, epic.ID); err != nil {
		t.Fatalf("SetParent A: %v", err)
	}
	if err := store.SetParent(ctx, b.ID, epic.ID); err != nil {
		t.Fatalf("SetParent B: %v", err)
	}

	if err := store.AddDependency(ctx, b.ID, a.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	ready, err := store.Ready(ctx, orgID, epic.ID)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != a.ID {
		t.Fatalf("want only A ready, got %+v", ready)
	}
}

func TestReadyIncludesUnblockedAfterDone(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "epic"})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}
	a, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "A"})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	b, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "B"})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if err := store.SetParent(ctx, a.ID, epic.ID); err != nil {
		t.Fatalf("SetParent A: %v", err)
	}
	if err := store.SetParent(ctx, b.ID, epic.ID); err != nil {
		t.Fatalf("SetParent B: %v", err)
	}
	if err := store.AddDependency(ctx, b.ID, a.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	if err := store.SetState(ctx, a.ID, "done"); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	ready, err := store.Ready(ctx, orgID, epic.ID)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != b.ID {
		t.Fatalf("want only B ready after A is done, got %+v", ready)
	}
}

func TestFailedBlockerKeepsDependentsBlocked(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "epic"})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}
	a, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "A"})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	b, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "B"})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if err := store.SetParent(ctx, a.ID, epic.ID); err != nil {
		t.Fatalf("SetParent A: %v", err)
	}
	if err := store.SetParent(ctx, b.ID, epic.ID); err != nil {
		t.Fatalf("SetParent B: %v", err)
	}
	if err := store.AddDependency(ctx, b.ID, a.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	if err := store.SetState(ctx, a.ID, "blocked"); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	ready, err := store.Ready(ctx, orgID, epic.ID)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	for _, item := range ready {
		if item.ID == b.ID {
			t.Fatalf("want B still blocked while A is blocked, got it in Ready: %+v", ready)
		}
	}
}

func TestCycleRejected(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	a, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "A"})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	b, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "B"})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	c, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "C"})
	if err != nil {
		t.Fatalf("Create C: %v", err)
	}

	// B depends on A, C depends on B.
	if err := store.AddDependency(ctx, b.ID, a.ID); err != nil {
		t.Fatalf("AddDependency B->A: %v", err)
	}
	if err := store.AddDependency(ctx, c.ID, b.ID); err != nil {
		t.Fatalf("AddDependency C->B: %v", err)
	}

	// Closing the cycle: A depends on C.
	err = store.AddDependency(ctx, a.ID, c.ID)
	if err == nil {
		t.Fatal("want error adding a dependency that closes a cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want error containing %q, got %q", "cycle", err.Error())
	}
}

func TestSelfDependencyRejected(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	a, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "A"})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}

	err = store.AddDependency(ctx, a.ID, a.ID)
	if err == nil {
		t.Fatal("want error for self-dependency")
	}
}

// TestOpenEpicsFindsDecomposedEpics pins the query the swarm scheduler
// ticks from. It is the one query in this package with no organization
// predicate — the scheduler is a platform worker — so the test also pins
// that it returns each epic paired with the organization the scheduler must
// re-enter before touching anything.
func TestOpenEpicsFindsDecomposedEpics(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "an epic"})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	child, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "a subtask"})
	if err != nil {
		t.Fatalf("create subtask: %v", err)
	}
	if err := store.SetParent(ctx, child.ID, epic.ID); err != nil {
		t.Fatalf("SetParent: %v", err)
	}

	// An item with no children is not an epic and must not be ticked.
	lone, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "bug", Goal: "not an epic"})
	if err != nil {
		t.Fatalf("create lone item: %v", err)
	}

	epics, err := store.OpenEpics(context.Background())
	if err != nil {
		t.Fatalf("OpenEpics: %v", err)
	}
	var foundEpic, foundLone bool
	for _, e := range epics {
		if e.ID == epic.ID {
			foundEpic = true
			if e.OrgID != orgID {
				t.Fatalf("epic came back under org %s, want %s", e.OrgID, orgID)
			}
		}
		if e.ID == lone.ID {
			foundLone = true
		}
	}
	if !foundEpic {
		t.Fatal("the decomposed epic is not listed, so the swarm scheduler would never tick it")
	}
	if foundLone {
		t.Fatal("an item with no subtasks was listed as an epic")
	}
}
