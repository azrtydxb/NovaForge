package agents_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
)

// TestOrphanedRunIsSettledAndReleasesItsLock pins crash recovery. A run is
// driven to its end by the replica executing it; when that replica dies, the
// row stays "running" forever — and because the branch lock is the run being
// running, a person would be locked out of the agent's branch forever too. A
// run still running well past its own wall-clock limit cannot still be
// executing (the loop enforces that limit as a deadline), so it is settled
// failed, saying why, and its branch is free.
func TestOrphanedRunIsSettledAndReleasesItsLock(t *testing.T) {
	pool := storePool(t)
	store := agents.NewStore(pool)
	lock := agents.NewBranchLock(pool)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)

	start := func(key string) agents.Run {
		run, err := store.CreateRun(ctx, agents.Run{
			OrgID: orgID, RepoID: repoID, AgentID: agent.ID, SponsorID: uuid.New(), GrantID: uuid.New(),
			Branch: "agents/" + key + "/work", WallclockLimit: time.Minute,
		})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := lock.Acquire(ctx, orgID, repoID, run.Branch, run.ID); err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		return run
	}
	orphan := start("NF-1")
	live := start("NF-2")
	// The orphan started two hours ago with a one-minute limit; its replica is
	// gone. The live run started just now.
	if _, err := pool.Exec(context.Background(),
		`UPDATE agents.agent_runs SET started_at = now() - interval '2 hours' WHERE id = $1`, orphan.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// The sweep crosses organizations on a database other suites share, so a
	// concurrent sweep may settle some stale run first and report the lost
	// race; what is asserted is the state of this test's own runs.
	if _, err := agents.RecoverOrphanedRuns(context.Background(), store, nil, 15*time.Minute); err != nil {
		t.Logf("RecoverOrphanedRuns reported: %v", err)
	}

	got, err := store.GetRun(ctx, orphan.ID)
	if err != nil {
		t.Fatalf("GetRun orphan: %v", err)
	}
	if got.State != "failed" || got.EndedAt == nil {
		t.Fatalf("orphan state = %q (ended %v), want failed with an end time", got.State, got.EndedAt)
	}
	if !strings.Contains(got.EndReason, "wall-clock") {
		t.Fatalf("orphan end reason = %q, want it to say why it was settled", got.EndReason)
	}
	if _, locked, err := lock.Check(ctx, repoID, "refs/heads/agents/NF-1/work"); err != nil || locked {
		t.Fatalf("the orphan's branch is still locked (locked=%v, err=%v)", locked, err)
	}

	still, err := store.GetRun(ctx, live.ID)
	if err != nil {
		t.Fatalf("GetRun live: %v", err)
	}
	if still.State != "running" {
		t.Fatalf("a run inside its wall-clock limit was settled: state %q", still.State)
	}
	if _, locked, _ := lock.Check(ctx, repoID, "agents/NF-2/other"); !locked {
		t.Fatal("a live run's prefix is not locked")
	}
}
