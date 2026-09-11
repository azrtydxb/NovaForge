package agents_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
)

func TestAcquireThenSecondAcquireFails(t *testing.T) {
	pool := storePool(t)
	store := agents.NewStore(pool)
	lock := agents.NewBranchLock(pool)

	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	agent := mustCreateAgent(t, store, ctx, orgID)
	run1, err := store.CreateRun(ctx, agents.Run{
		OrgID:     orgID,
		AgentID:   agent.ID,
		SponsorID: uuid.New(),
		GrantID:   uuid.New(),
		Branch:    "agents/NF-1/work",
	})
	if err != nil {
		t.Fatalf("CreateRun run1: %v", err)
	}
	run2, err := store.CreateRun(ctx, agents.Run{
		OrgID:     orgID,
		AgentID:   agent.ID,
		SponsorID: uuid.New(),
		GrantID:   uuid.New(),
		Branch:    "agents/NF-1/work",
	})
	if err != nil {
		t.Fatalf("CreateRun run2: %v", err)
	}

	if err := lock.Acquire(ctx, orgID, repoID, "agents/NF-1/work", run1.ID); err != nil {
		t.Fatalf("Acquire run1: %v", err)
	}

	err = lock.Acquire(ctx, orgID, repoID, "agents/NF-1/work", run2.ID)
	if err == nil {
		t.Fatal("expected second Acquire to fail")
	}
	if !strings.Contains(err.Error(), "locked by run") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "locked by run")
	}
}

func TestHumanPushRejectedWhileLocked(t *testing.T) {
	pool := storePool(t)
	store := agents.NewStore(pool)
	lock := agents.NewBranchLock(pool)

	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	agent := mustCreateAgent(t, store, ctx, orgID)
	run, err := store.CreateRun(ctx, agents.Run{
		OrgID:     orgID,
		AgentID:   agent.ID,
		SponsorID: uuid.New(),
		GrantID:   uuid.New(),
		Branch:    "agents/NF-1/work",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := lock.Acquire(ctx, orgID, repoID, "agents/NF-1/work", run.ID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	capFunc := lock.CapFunc(repoID, nil)
	humanScope := authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"}

	err = capFunc(ctx, humanScope, orgID, "example-repo", []string{"refs/heads/agents/NF-1/work"})
	if err == nil {
		t.Fatal("expected a human push to a locked branch to be rejected")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "locked")
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	pool := storePool(t)
	store := agents.NewStore(pool)
	lock := agents.NewBranchLock(pool)

	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	agent := mustCreateAgent(t, store, ctx, orgID)
	run1, err := store.CreateRun(ctx, agents.Run{
		OrgID: orgID, AgentID: agent.ID, SponsorID: uuid.New(), GrantID: uuid.New(),
		Branch: "agents/NF-2/work",
	})
	if err != nil {
		t.Fatalf("CreateRun run1: %v", err)
	}
	run2, err := store.CreateRun(ctx, agents.Run{
		OrgID: orgID, AgentID: agent.ID, SponsorID: uuid.New(), GrantID: uuid.New(),
		Branch: "agents/NF-2/work",
	})
	if err != nil {
		t.Fatalf("CreateRun run2: %v", err)
	}

	if err := lock.Acquire(ctx, orgID, repoID, "agents/NF-2/work", run1.ID); err != nil {
		t.Fatalf("Acquire run1: %v", err)
	}
	if err := lock.Release(ctx, orgID, repoID, "agents/NF-2/work"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := lock.Acquire(ctx, orgID, repoID, "agents/NF-2/work", run2.ID); err != nil {
		t.Fatalf("Acquire run2 after release: %v", err)
	}

	holder, locked, err := lock.Holder(ctx, orgID, repoID, "agents/NF-2/work")
	if err != nil {
		t.Fatalf("Holder: %v", err)
	}
	if !locked || holder != run2.ID {
		t.Fatalf("Holder = (%s, %v), want (%s, true)", holder, locked, run2.ID)
	}
}

func TestLockExpiresWithRun(t *testing.T) {
	pool := storePool(t)
	store := agents.NewStore(pool)
	lock := agents.NewBranchLock(pool)

	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	agent := mustCreateAgent(t, store, ctx, orgID)
	run, err := store.CreateRun(ctx, agents.Run{
		OrgID: orgID, AgentID: agent.ID, SponsorID: uuid.New(), GrantID: uuid.New(),
		Branch: "agents/NF-3/work",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := lock.Acquire(ctx, orgID, repoID, "agents/NF-3/work", run.ID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	if _, locked, err := lock.Holder(ctx, orgID, repoID, "agents/NF-3/work"); err != nil || !locked {
		t.Fatalf("Holder before terminal state = (locked=%v, err=%v), want locked", locked, err)
	}

	if err := store.SetRunState(ctx, run.ID, "succeeded"); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}

	_, locked, err := lock.Holder(ctx, orgID, repoID, "agents/NF-3/work")
	if err != nil {
		t.Fatalf("Holder: %v", err)
	}
	if locked {
		t.Fatal("expected the lock to be treated as released once the run reached a terminal state")
	}
}
