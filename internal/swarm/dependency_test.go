package swarm_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/swarm"
	"github.com/novaforge/novaforge/internal/work"
)

// TestSwarmDependencyOrder is spec S-19: an epic decomposes into
// dependency-ordered subtasks assigned to specialized agents, and a failed
// prerequisite blocks its dependents.
//
// Two things the earlier tests did not show are asserted here. Every subtask
// is started with the agent whose role the decomposition gave it. And the
// failure comes from where it comes from in production — the subtask's Agent
// Run ending failed, read through Scheduler.RunOutcome — not from a test
// writing "blocked" onto the subtask itself: nothing in production used to
// write it, so a failed run left its subtask in_progress forever and its
// dependents waiting on something that would never finish.
func TestSwarmDependencyOrder(t *testing.T) {
	store := newWorkStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	epic, byKey := decomposedEpic(t, store, ctx, orgID, uuid.New())

	roleAgents := allRolesEnabled()
	var mu sync.Mutex
	startedWith := map[uuid.UUID]agents.Agent{}
	// runState is what agent-runtime would report for each subtask's run.
	runState := map[uuid.UUID]string{}
	sch := &swarm.Scheduler{
		Work:      store,
		RoleAgent: roleAgents,
		StartRun: func(ctx context.Context, subtask work.Item, agent agents.Agent) (agents.Run, error) {
			mu.Lock()
			defer mu.Unlock()
			if _, again := startedWith[subtask.ID]; again {
				t.Errorf("subtask %s started twice", subtask.Key)
			}
			startedWith[subtask.ID] = agent
			runState[subtask.ID] = "running"
			return agents.Run{ID: uuid.New(), AgentID: agent.ID}, nil
		},
		RunOutcome: func(ctx context.Context, subtask work.Item) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return runState[subtask.ID], nil
		},
	}
	tick := func() {
		t.Helper()
		if _, err := sch.Tick(ctx, epic.ID); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	state := func(key string) string {
		t.Helper()
		it, err := store.Get(ctx, byKey[key].ID)
		if err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
		return it.State
	}
	started := func(key string) bool {
		mu.Lock()
		defer mu.Unlock()
		_, ok := startedWith[byKey[key].ID]
		return ok
	}
	// finish makes a subtask's run succeed, and nothing more: the scheduler
	// itself must finish the subtask, or its dependents wait forever.
	finish := func(key string) {
		t.Helper()
		mu.Lock()
		runState[byKey[key].ID] = "succeeded"
		mu.Unlock()
	}

	// Only the dependency-free subtask starts, and a run still going leaves it
	// in progress.
	tick()
	tick()
	if !started("db") || started("oauth-backend") || state("db") != "in_progress" {
		t.Fatalf("after two ticks: db started=%v state=%s, oauth-backend started=%v", started("db"), state("db"), started("oauth-backend"))
	}

	finish("db")
	tick()
	if got := state("db"); got != "done" {
		t.Fatalf("db state = %q after its run succeeded, want done", got)
	}
	finish("oauth-backend")
	tick()
	if !started("admin-config") || !started("frontend") {
		t.Fatal("admin-config and frontend did not start once oauth-backend was done")
	}

	// frontend's Agent Run fails.
	mu.Lock()
	runState[byKey["frontend"].ID] = "over_budget"
	mu.Unlock()
	tick()
	if got := state("frontend"); got != "blocked" {
		t.Fatalf("frontend state = %q after its run ended over budget, want blocked", got)
	}
	if got := state("admin-config"); got != "in_progress" {
		t.Fatalf("admin-config state = %q, want in_progress: its run is still going", got)
	}
	ep, err := store.Get(ctx, epic.ID)
	if err != nil {
		t.Fatalf("Get epic: %v", err)
	}
	if ep.State != "blocked" {
		t.Fatalf("epic state = %q, want blocked once a subtask is blocked", ep.State)
	}

	// docs depends on admin-config and frontend. admin-config finishing does
	// not start it: its other prerequisite failed.
	finish("admin-config")
	tick()
	tick()
	if started("docs") || started("integration-tests") {
		t.Fatal("a dependent of the failed frontend subtask was started")
	}

	// Every subtask that started, started with the agent for its role.
	mu.Lock()
	defer mu.Unlock()
	if len(startedWith) != 4 {
		t.Fatalf("started %d subtasks, want 4 (db, oauth-backend, admin-config, frontend)", len(startedWith))
	}
	for id, agent := range startedWith {
		role, err := store.SubtaskRole(ctx, id)
		if err != nil {
			t.Fatalf("SubtaskRole: %v", err)
		}
		if agent.Role != role || agent.ID != roleAgents[role].ID {
			t.Fatalf("subtask %s (role %s) started with agent %s of role %s", id, role, agent.Name, agent.Role)
		}
	}
}
