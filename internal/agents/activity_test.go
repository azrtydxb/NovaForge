package agents_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
)

// Section 24 wants the product to say what each agent is doing — "Backend
// Engineer working NF-183", "Architect idle" — and the platform could not answer
// it: an agent carried a name, a role and a model, and nothing joined it to the
// run it was executing.
//
// The Work Item key comes from the run's own frozen snapshot, not from the Work
// service's tables: an agent's activity must be answerable without reading
// another service's schema.
func TestAgentActivityReported(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	idle := mustCreateAgent(t, store, ctx, orgID)
	busy := mustCreateAgent(t, store, ctx, orgID)

	repoID, itemID := uuid.New(), uuid.New()
	run, err := store.CreateRun(ctx, agents.Run{
		OrgID: orgID, AgentID: busy.ID, RepoID: repoID, WorkItemID: itemID,
		SponsorID: uuid.New(), GrantID: uuid.New(),
		Branch:         uniqueName("agents/NF-77/work"),
		WallclockLimit: time.Hour, TokenLimit: 1_000_000, CostLimitMicros: 5_000_000,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := store.FreezeWorkItem(ctx, run, &workv1.WorkItem{
		Id: itemID.String(), RepoId: repoID.String(), Key: "NF-77", Goal: "the work in hand",
	}); err != nil {
		t.Fatalf("FreezeWorkItem: %v", err)
	}
	if err := store.SetRunState(ctx, run.ID, "running"); err != nil {
		t.Fatalf("SetRunState running: %v", err)
	}

	activity := func() map[uuid.UUID]agents.Activity {
		t.Helper()
		list, err := store.ListAgentActivity(ctx)
		if err != nil {
			t.Fatalf("ListAgentActivity: %v", err)
		}
		by := make(map[uuid.UUID]agents.Activity, len(list))
		for _, a := range list {
			by[a.AgentID] = a
		}
		return by
	}

	got := activity()
	if a, ok := got[busy.ID]; !ok {
		t.Fatal("the executing agent is not reported at all")
	} else if a.RunID != run.ID || a.WorkItemKey != "NF-77" || a.Since.IsZero() {
		t.Fatalf("the executing agent reports %+v, want run %s on NF-77 with a start time", a, run.ID)
	}
	if a, ok := got[idle.ID]; !ok {
		t.Fatal("an idle agent is not reported at all; the roster must list every agent")
	} else if a.RunID != uuid.Nil || a.WorkItemKey != "" {
		t.Fatalf("an agent holding no run reports %+v, want idle", a)
	}

	// A finished run leaves the agent idle again, so the roster does not keep
	// claiming work that has ended.
	if err := store.SetRunState(ctx, run.ID, "succeeded"); err != nil {
		t.Fatalf("SetRunState succeeded: %v", err)
	}
	if a := activity()[busy.ID]; a.RunID != uuid.Nil || a.WorkItemKey != "" {
		t.Fatalf("after its run ended the agent still reports %+v, want idle", a)
	}
}

// The roster is org-scoped like every other query.
func TestAgentActivityIsOrganizationScoped(t *testing.T) {
	store := newStore(t)
	mine, theirs := uuid.New(), uuid.New()
	mustCreateAgent(t, store, scopedCtx(theirs), theirs)

	list, err := store.ListAgentActivity(scopedCtx(mine))
	if err != nil {
		t.Fatalf("ListAgentActivity: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("another organization's agents appear in this one's roster: %+v", list)
	}
}
