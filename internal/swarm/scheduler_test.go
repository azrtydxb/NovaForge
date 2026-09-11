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

// decomposedEpic sets up an epic with the six-subtask SSO decomposition
// already materialised, returning the epic and the materialised subtasks
// keyed by their planner key.
func decomposedEpic(t *testing.T, store *work.Store, ctx context.Context, orgID, repoID uuid.UUID) (work.Item, map[string]work.Item) {
	t.Helper()
	epic, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "Add enterprise SSO"})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}

	model := &stubPlannerModel{body: ssoDecomposition}
	p := swarm.NewPlanner(model, store, ssoRoles)
	subs, err := p.Decompose(ctx, epic, swarm.Bundle{})
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	items, err := p.Materialise(ctx, epic, subs)
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}

	byKey := make(map[string]work.Item, len(subs))
	for i, s := range subs {
		byKey[s.Key] = items[i]
	}
	return epic, byKey
}

// recordingStarter is a StartRun stub that records every subtask it was
// asked to start and always succeeds, returning a distinct fake Run each
// time.
func recordingStarter() (func(ctx context.Context, subtask work.Item, agent agents.Agent) (agents.Run, error), *[]uuid.UUID) {
	var mu sync.Mutex
	var started []uuid.UUID
	fn := func(ctx context.Context, subtask work.Item, agent agents.Agent) (agents.Run, error) {
		mu.Lock()
		defer mu.Unlock()
		started = append(started, subtask.ID)
		return agents.Run{ID: uuid.New(), AgentID: agent.ID}, nil
	}
	return fn, &started
}

// allRolesEnabled builds a RoleAgent map with one distinct fake enabled
// agent per role in ssoRoles.
func allRolesEnabled() map[string]agents.Agent {
	out := make(map[string]agents.Agent, len(ssoRoles))
	for _, role := range ssoRoles {
		out[role] = agents.Agent{ID: uuid.New(), Name: "agent-" + role, Role: role, Enabled: true}
	}
	return out
}

func TestTickStartsOnlyReadySubtasks(t *testing.T) {
	store := newWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, _ := decomposedEpic(t, store, ctx, orgID, repoID)

	startFn, started := recordingStarter()
	sch := &swarm.Scheduler{
		Work:      store,
		RoleAgent: allRolesEnabled(),
		StartRun:  startFn,
	}

	n, err := sch.Tick(ctx, epic.ID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 run started (only the dependency-free \"db\" subtask), got %d", n)
	}
	if len(*started) != 1 {
		t.Fatalf("want 1 recorded start, got %d", len(*started))
	}
}

func TestBlockedDependentNotStarted(t *testing.T) {
	store := newWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, byKey := decomposedEpic(t, store, ctx, orgID, repoID)

	startFn, started := recordingStarter()
	sch := &swarm.Scheduler{
		Work:      store,
		RoleAgent: allRolesEnabled(),
		StartRun:  startFn,
	}

	if _, err := sch.Tick(ctx, epic.ID); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	oauthID := byKey["oauth-backend"].ID
	for _, id := range *started {
		if id == oauthID {
			t.Fatal("want oauth-backend not started while its db prerequisite is unfinished")
		}
	}
}

func TestFailedPrerequisiteBlocksDependents(t *testing.T) {
	store := newWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, byKey := decomposedEpic(t, store, ctx, orgID, repoID)

	startFn, started := recordingStarter()
	sch := &swarm.Scheduler{
		Work:      store,
		RoleAgent: allRolesEnabled(),
		StartRun:  startFn,
	}

	// db starts on the first tick...
	if _, err := sch.Tick(ctx, epic.ID); err != nil {
		t.Fatalf("Tick (1st): %v", err)
	}
	// ...and then fails.
	if err := store.SetState(ctx, byKey["db"].ID, "blocked"); err != nil {
		t.Fatalf("SetState db blocked: %v", err)
	}

	n, err := sch.Tick(ctx, epic.ID)
	if err != nil {
		t.Fatalf("Tick (2nd): %v", err)
	}
	if n != 0 {
		t.Fatalf("want 0 runs started once the db prerequisite is blocked, got %d", n)
	}

	oauthID := byKey["oauth-backend"].ID
	for _, id := range *started {
		if id == oauthID {
			t.Fatal("want oauth-backend never started: its prerequisite failed")
		}
	}

	got, err := store.Get(ctx, epic.ID)
	if err != nil {
		t.Fatalf("Get epic: %v", err)
	}
	if got.State != "blocked" {
		t.Fatalf("want epic state \"blocked\" once a subtask is blocked, got %q", got.State)
	}
}

func TestTickIsIdempotent(t *testing.T) {
	store := newWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, _ := decomposedEpic(t, store, ctx, orgID, repoID)

	startFn, _ := recordingStarter()
	sch := &swarm.Scheduler{
		Work:      store,
		RoleAgent: allRolesEnabled(),
		StartRun:  startFn,
	}

	if _, err := sch.Tick(ctx, epic.ID); err != nil {
		t.Fatalf("Tick (1st): %v", err)
	}
	n, err := sch.Tick(ctx, epic.ID)
	if err != nil {
		t.Fatalf("Tick (2nd): %v", err)
	}
	if n != 0 {
		t.Fatalf("want 0 runs started by a 2nd tick with no state change in between, got %d", n)
	}
}

func TestConcurrencyCapRespected(t *testing.T) {
	store := newWorkStore(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	epic, err := store.Create(ctx, work.Item{OrgID: orgID, RepoID: repoID, Type: "feature", Goal: "many independent subtasks"})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}

	model := &stubPlannerModel{body: `{"elements":[
		{"key":"s1","title":"S1","goal":"g","type":"feature","agentRole":"backend","dependsOn":[]},
		{"key":"s2","title":"S2","goal":"g","type":"feature","agentRole":"backend","dependsOn":[]},
		{"key":"s3","title":"S3","goal":"g","type":"feature","agentRole":"backend","dependsOn":[]},
		{"key":"s4","title":"S4","goal":"g","type":"feature","agentRole":"backend","dependsOn":[]},
		{"key":"s5","title":"S5","goal":"g","type":"feature","agentRole":"backend","dependsOn":[]}
	]}`}
	p := swarm.NewPlanner(model, store, []string{"backend"})
	subs, err := p.Decompose(ctx, epic, swarm.Bundle{})
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if _, err := p.Materialise(ctx, epic, subs); err != nil {
		t.Fatalf("Materialise: %v", err)
	}

	startFn, started := recordingStarter()
	sch := &swarm.Scheduler{
		Work:              store,
		MaxConcurrentRuns: 2,
		RoleAgent:         map[string]agents.Agent{"backend": {ID: uuid.New(), Role: "backend", Enabled: true}},
		StartRun:          startFn,
	}

	n, err := sch.Tick(ctx, epic.ID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 2 {
		t.Fatalf("want exactly 2 runs started (the concurrency cap), got %d", n)
	}
	if len(*started) != 2 {
		t.Fatalf("want 2 recorded starts, got %d", len(*started))
	}
}
