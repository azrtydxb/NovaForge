package agents_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/capability"
)

// recoverRun runs the platform-wide orphan recovery and fails only when it
// could not reconcile this run.
//
// RecoverOrphanedRuns scans every organization and reports every run still
// holding a resource, so its error is about the whole deployment, not about the
// caller's run. Asserting it returns nil only holds on a database no other run
// has ever touched — and these suites run against a shared cluster datastore
// where other tests deliberately leave runs with cleanup outstanding
// (TestPurgePreservesUnconfirmedGrantCleanup is one). That made this test fail
// on other tests' leftovers rather than on its own behaviour.
func recoverRun(t *testing.T, store *agents.Store, id uuid.UUID) {
	t.Helper()
	_, err := agents.RecoverOrphanedRuns(context.Background(), store, nil, agents.OrphanGrace)
	if err != nil && strings.Contains(err.Error(), id.String()) {
		t.Fatalf("recovery did not reconcile run %s: %v", id, err)
	}
}

func TestTerminalGrantCleanupRetriesWithoutRerunning(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	srv, store := newGRPCServer(t, &stubWorkClient{item: &workv1.WorkItem{Id: uuid.NewString(), Key: "NF-99", RepoId: repoID.String()}})
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)
	srv.Execute = func(ctx context.Context, run agents.Run) {
		<-ctx.Done()
		_, _ = store.CompleteRun(context.WithoutCancel(ctx), run.ID, agents.Completion{State: "cancelled"})
	}
	resp, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{AgentId: agent.ID.String(), RepoId: repoID.String(), WorkItemKey: "NF-99", SponsorId: actorOf(ctx)})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.MustParse(resp.GetRun().GetId())
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, _ := store.GetRun(ctx, id)
		if r.State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run not started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	revoke := store.GrantIssuanceCanceller
	store.GrantIssuanceCanceller = func(context.Context, capability.IssuanceIntent) error { return fmt.Errorf("grant owner unavailable") }
	if _, err = srv.CancelRun(ctx, &agentsv1.CancelRunRequest{Id: id.String()}); err == nil {
		t.Fatal("claimed confirmed cleanup while grant owner unavailable")
	}
	run, err := store.GetRun(ctx, id)
	if err != nil || run.State != "cancelled" || !run.GrantCleanupPending || run.GrantCleanupError == "" {
		t.Fatalf("cleanup intent not durable: %+v %v", run, err)
	}
	store.GrantIssuanceCanceller = revoke
	time.Sleep(1100 * time.Millisecond) // persisted retry delay
	recoverRun(t, store, id)
	run, err = store.GetRun(ctx, id)
	if err != nil || run.State != "cancelled" || run.GrantCleanupPending {
		t.Fatalf("cleanup not reconciled: %+v %v", run, err)
	}
	if _, err := capability.NewStore(store.Pool()).Resolve(ctx, run.GrantID); err == nil {
		t.Fatal("cancelled run's grant is still active")
	}
	// A second recovery has nothing to resurrect or rewrite.
	recoverRun(t, store, id)
}

func TestPurgePreservesUnconfirmedGrantCleanup(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	srv, store := newGRPCServer(t, &stubWorkClient{item: &workv1.WorkItem{Id: uuid.NewString(), Key: "NF-97", RepoId: repoID.String()}})
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)
	// Issue through the real owner store; this row is deliberately queued so
	// purge must fence work before attempting remote cleanup.
	grant, err := srv.Grants.Issue(ctx, capability.Grant{OrgID: orgID, SubjectID: agent.ID, SubjectKind: "agent", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, agents.Run{OrgID: orgID, RepoID: repoID, AgentID: agent.ID, SponsorID: uuid.New(), GrantID: grant.ID, Branch: "agents/NF-97/work"})
	if err != nil {
		t.Fatal(err)
	}
	revoke := store.GrantRevoker
	store.GrantRevoker = nil
	if _, err := store.PurgeRuns(ctx, &repoID); err == nil {
		t.Fatal("purged without revoker")
	}
	saved, err := store.GetRun(ctx, run.ID)
	if err != nil || saved.State != "cancelled" || !saved.GrantCleanupPending {
		t.Fatalf("lost cleanup intent: %+v %v", saved, err)
	}
	store.GrantRevoker = revoke
	if _, err := store.PurgeRuns(ctx, &repoID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRun(ctx, run.ID); err == nil {
		t.Fatal("confirmed run retained")
	}
	if _, err := capability.NewStore(store.Pool()).Resolve(ctx, grant.ID); err == nil {
		t.Fatal("purge left active authority")
	}
}

func TestCleanupFairAcrossReplicas(t *testing.T) {
	store := agents.NewStore(storePool(t))
	org := uuid.New()
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	var newest uuid.UUID
	for i := 0; i < 101; i++ {
		r, err := store.CreateRun(ctx, agents.Run{OrgID: org, RepoID: uuid.New(), AgentID: agent.ID, SponsorID: uuid.New(), GrantID: uuid.New(), Branch: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
		if err = store.SetRunState(ctx, r.ID, "failed"); err != nil {
			t.Fatal(err)
		}
		newest = r.GrantID
	}
	var mu sync.Mutex
	calls := map[uuid.UUID]int{}
	revoke := func(ctx context.Context, id uuid.UUID) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("owner call unbounded")
		}
		mu.Lock()
		calls[id]++
		mu.Unlock()
		time.Sleep(time.Millisecond)
		if id == newest {
			return nil
		}
		return fmt.Errorf("owner unavailable")
	}
	store.GrantRevoker = revoke
	replica := agents.NewStore(store.Pool())
	replica.GrantRevoker = revoke
	var wg sync.WaitGroup
	for _, s := range []*agents.Store{store, replica} {
		wg.Add(1)
		go func(s *agents.Store) { defer wg.Done(); _ = s.ReconcileGrantCleanup(context.Background()) }(s)
	}
	wg.Wait()
	_ = store.ReconcileGrantCleanup(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if calls[newest] != 1 {
		t.Errorf("101st grant attempted %d times, want once", calls[newest])
	}
	for id, n := range calls {
		if n > 2 {
			t.Errorf("grant %s monopolized claims: %d calls", id, n)
		}
	}
}

func TestCleanupAcknowledgementRequiresCurrentClaim(t *testing.T) {
	for _, cleared := range []bool{false, true} {
		t.Run(fmt.Sprint(cleared), func(t *testing.T) {
			org, repo := uuid.New(), uuid.New()
			srv, store := newGRPCServer(t, &stubWorkClient{})
			ctx := scopedCtx(org)
			agent := mustCreateAgent(t, store, ctx, org)
			grant, err := srv.Grants.Issue(ctx, capability.Grant{OrgID: org, SubjectID: agent.ID, SubjectKind: "agent", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.CreateRun(ctx, agents.Run{OrgID: org, RepoID: repo, AgentID: agent.ID, SponsorID: uuid.New(), GrantID: grant.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.SetRunState(ctx, run.ID, "failed"); err != nil {
				t.Fatal(err)
			}
			paused, resume := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(resume) }) }
			defer release()
			revoke := store.GrantRevoker
			store.GrantRevoker = func(call context.Context, id uuid.UUID) error {
				if err := revoke(call, id); err != nil {
					return err
				}
				close(paused)
				<-resume
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- store.CleanupRunGrant(ctx, run.ID) }()
			<-paused
			// Expire A's owned lease, then let a second Store actually claim
			// and revoke the real grant. B stays paused before its own ack.
			if _, err = store.Pool().Exec(ctx, `UPDATE agents.agent_runs SET grant_cleanup_lease_until=now()-interval '1 second',grant_cleanup_error='replica B owns cleanup' WHERE id=$1 AND org_id=$2`, run.ID, org); err != nil {
				t.Fatal(err)
			}
			replica := agents.NewStore(store.Pool())
			bPaused, bResume := make(chan struct{}), make(chan struct{})
			var bOnce sync.Once
			releaseB := func() { bOnce.Do(func() { close(bResume) }) }
			defer releaseB()
			replica.GrantRevoker = func(call context.Context, id uuid.UUID) error {
				if err := revoke(call, id); err != nil {
					return err
				}
				close(bPaused)
				if !cleared {
					<-bResume
				}
				return nil
			}
			bDone := make(chan error, 1)
			go func() { bDone <- replica.CleanupRunGrant(ctx, run.ID) }()
			<-bPaused
			if cleared {
				if err = <-bDone; err != nil {
					t.Fatal(err)
				}
			}
			var bClaim uuid.UUID
			if err = store.Pool().QueryRow(ctx, `SELECT COALESCE(grant_cleanup_claim,'00000000-0000-0000-0000-000000000000') FROM agents.agent_runs WHERE id=$1 AND org_id=$2`, run.ID, org).Scan(&bClaim); err != nil {
				t.Fatal(err)
			}

			release()
			err = <-done
			if !cleared && err == nil {
				t.Error("stale acknowledgement reported confirmed cleanup")
			}
			if cleared && err != nil {
				t.Errorf("already confirmed obligations rejected: %v", err)
			}
			saved, e := store.GetRun(ctx, run.ID)
			wantError := "replica B owns cleanup"
			if cleared {
				wantError = ""
			}
			if e != nil || saved.GrantCleanupPending == cleared || saved.GrantCleanupError != wantError {
				t.Errorf("replica B state overwritten: %+v %v", saved, e)
			}
			var after uuid.UUID
			if err = store.Pool().QueryRow(ctx, `SELECT COALESCE(grant_cleanup_claim,'00000000-0000-0000-0000-000000000000') FROM agents.agent_runs WHERE id=$1 AND org_id=$2`, run.ID, org).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != bClaim {
				t.Error("stale acknowledgement overwrote B's claim")
			}
			releaseB()
			if !cleared {
				if err = <-bDone; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
