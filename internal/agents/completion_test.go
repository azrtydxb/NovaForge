package agents_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/agents"
)

func TestCompletionRetryAndRecoveryRace(t *testing.T) {
	store := agents.NewStore(storePool(t))
	org := uuid.New()
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	for _, cancelFirst := range []bool{false, true} {
		run, err := store.CreateRun(ctx, agents.Run{OrgID: org, AgentID: agent.ID, SponsorID: uuid.New(), WallclockLimit: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if err = store.SetRunState(ctx, run.ID, "running"); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Pool().Exec(ctx, `UPDATE agents.agent_runs SET started_at=now()-interval '1 hour' WHERE id=$1`, run.ID); err != nil {
			t.Fatal(err)
		}
		if cancelFirst {
			if err = store.SetRunState(ctx, run.ID, "cancelled"); err != nil {
				t.Fatal(err)
			}
		}
		receipt := agents.Completion{State: "succeeded", Summary: "completed", Spend: agents.Spend{Tokens: 91, CostMicros: 13, TokensAvailable: true, CostAvailable: true}}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = agents.RecoverOrphanedRuns(context.Background(), store, nil, 0) }()
		go func() {
			defer wg.Done()
			if _, err := store.CompleteRun(ctx, run.ID, receipt); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		// The commit may have succeeded when the client lost its response. Identical
		// delivery retries must not duplicate the summary or cleanup work.
		if _, err = store.CompleteRun(ctx, run.ID, receipt); err != nil {
			t.Fatal(err)
		}
		if err = store.RecordSpend(ctx, run.ID, agents.Spend{Reason: "stale writer"}); err != nil {
			t.Fatal(err)
		}
		saved, err := store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.TokensUsed != 91 || saved.CostUsedMicros != 13 || !saved.TokensAvailable || !saved.CostAvailable {
			t.Fatalf("accounting overwritten: %+v", saved)
		}
		if cancelFirst && saved.State != "cancelled" {
			t.Fatal("completion resurrected cancelled run")
		}
		entries, err := agents.NewAuditLog(store.Pool()).List(ctx, run.ID)
		if err != nil || len(entries) != 1 {
			t.Fatalf("duplicate evidence: %v %v", entries, err)
		}
		receipt.Spend.Tokens++
		if _, err = store.CompleteRun(ctx, run.ID, receipt); err == nil {
			t.Fatal("conflicting receipt accepted")
		}
		if _, err = store.CompleteRun(scopedCtx(uuid.New()), run.ID, receipt); err == nil {
			t.Fatal("foreign receipt accepted")
		}
	}
}
