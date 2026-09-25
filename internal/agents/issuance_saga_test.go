package agents_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
)

type observedOwner struct {
	ownerFixture
	issue func(context.Context, capability.IssuanceIntent) (capability.Grant, error)
}

func (o observedOwner) IssueIntent(ctx context.Context, i capability.IssuanceIntent) (capability.Grant, error) {
	return o.issue(ctx, i)
}

func TestAdmissionIntentPrecedesAuthorityAndCompensatesLostReply(t *testing.T) {
	org, repo := uuid.New(), uuid.New()
	item := &workv1.WorkItem{Id: uuid.NewString(), Key: "NF-10", RepoId: repo.String(), Goal: "frozen", Acceptance: []string{"original"}}
	srv, store := newGRPCServer(t, &stubWorkClient{item: item})
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	owner := srv.Grants.(ownerFixture)
	var issuedID uuid.UUID
	var executed atomic.Bool
	srv.Execute = func(context.Context, agents.Run) { executed.Store(true) }
	srv.Grants = observedOwner{ownerFixture: owner, issue: func(call context.Context, i capability.IssuanceIntent) (capability.Grant, error) {
		saved, err := store.GetRun(ctx, i.RunID)
		if err != nil || saved.GrantIntent == nil {
			t.Fatalf("authority before durable intent: %+v %v", saved, err)
		}
		frozen, err := saved.FrozenWorkItem()
		if err != nil || frozen.GetAcceptance()[0] != "original" {
			t.Fatalf("authority before frozen Work: %v %v", frozen, err)
		}
		grant, err := owner.IssueIntent(call, i)
		if err != nil {
			return grant, err
		}
		issuedID = grant.ID
		return capability.Grant{}, fmt.Errorf("lost successful owner response")
	}}
	_, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{AgentId: agent.ID.String(), RepoId: repo.String(), WorkItemKey: item.Key, SponsorId: actorOf(ctx)})
	if err == nil || issuedID == uuid.Nil || executed.Load() {
		t.Fatalf("uncertain authority executed: %v %s", err, issuedID)
	}
	if _, err = owner.Resolve(ctx, issuedID); err == nil {
		t.Fatal("lost-reply authority not revoked")
	}
	ids, err := store.RunIDsToPurge(ctx, &repo)
	if err != nil || len(ids) != 1 {
		t.Fatalf("intent lost: %v %v", ids, err)
	}
	saved, _ := store.GetRun(ctx, ids[0])
	if saved.State != "failed" || saved.GrantCleanupPending || saved.WorkReleasePending {
		t.Fatalf("compensation incomplete: %+v", saved)
	}
}

func TestIssuanceRecoveryAndLateReplyAreFenced(t *testing.T) {
	for _, issueFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(issueFirst), func(t *testing.T) {
			org, repo := uuid.New(), uuid.New()
			item := &workv1.WorkItem{Id: uuid.NewString(), Key: "NF-11", RepoId: repo.String()}
			srv, store := newGRPCServer(t, &stubWorkClient{item: item})
			ctx := scopedCtx(org)
			agent := mustCreateAgent(t, store, ctx, org)
			scope, _ := authz.FromContext(ctx)
			intent := capability.IssuanceIntent{IssuerID: scope.ActorID, IssuerKind: scope.ActorKind, Grant: capability.Grant{ID: uuid.New(), OrgID: org, SubjectID: agent.ID, SubjectKind: "agent", RepoRead: true, WriteBranch: "agents/NF-11/", ExpiresAt: time.Now().Add(time.Hour)}}
			run, err := store.CreateRun(ctx, agents.Run{OrgID: org, RepoID: repo, AgentID: agent.ID, SponsorID: scope.ActorID, WorkItemID: uuid.MustParse(item.Id), GrantID: intent.Grant.ID, GrantIntent: &intent, WorkClaimRequired: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.WorkClaims.ClaimExecution(ctx, run.ExecutionClaim()); err != nil {
				t.Fatal(err)
			}
			owner := srv.Grants.(ownerFixture)
			if issueFirst {
				if _, err = owner.IssueIntent(ctx, *run.GrantIntent); err != nil {
					t.Fatal(err)
				}
			}
			// Crash before local acknowledgement. The live admission lease must first
			// protect it from another replica; after expiry a new store retries cleanup.
			if _, err = agents.RecoverOrphanedRuns(context.Background(), store, nil, agents.OrphanGrace); err != nil {
				t.Fatal(err)
			}
			before, _ := store.GetRun(ctx, run.ID)
			if before.State != "queued" {
				t.Fatal("live admission failed by peer")
			}
			if _, err = store.Pool().Exec(ctx, `UPDATE agents.agent_runs SET admission_lease_until=now()-interval '1 second' WHERE id=$1 AND org_id=$2`, run.ID, org); err != nil {
				t.Fatal(err)
			}
			restarted := agents.NewStore(store.Pool())
			restarted.GrantRevoker = owner.Revoke
			restarted.GrantIssuanceCanceller = owner.CancelIssuance
			restarted.WorkClaims = store.WorkClaims
			if _, err = agents.RecoverOrphanedRuns(context.Background(), restarted, nil, agents.OrphanGrace); err != nil {
				t.Fatal(err)
			}
			saved, _ := store.GetRun(ctx, run.ID)
			if saved.State != "failed" || saved.GrantCleanupPending || saved.WorkReleasePending {
				t.Fatalf("restart lost obligations: %+v", saved)
			}
			if _, err = owner.IssueIntent(ctx, *run.GrantIntent); err == nil {
				t.Fatal("late issue resurrected authority")
			}
			if _, err = store.WorkClaims.ClaimExecution(ctx, run.ExecutionClaim()); err == nil {
				t.Fatal("late Work claim resurrected execution")
			}
			if err = agents.NewBranchLock(store.Pool()).Acquire(ctx, org, repo, run.Branch, run.ID); err == nil {
				t.Fatal("late response started terminal run")
			}
		})
	}
}

func TestRunningOrphanCannotReleaseWorkByElapsedTime(t *testing.T) {
	org, repo := uuid.New(), uuid.New()
	item := &workv1.WorkItem{Id: uuid.NewString(), Key: "NF-21", RepoId: repo.String()}
	srv, store := newGRPCServer(t, &stubWorkClient{item: item})
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	running := make(chan agents.Run, 1)
	srv.Execute = func(_ context.Context, r agents.Run) { running <- r }
	if _, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{AgentId: agent.ID.String(), RepoId: repo.String(), WorkItemKey: item.Key, SponsorId: actorOf(ctx)}); err != nil {
		t.Fatal(err)
	}
	run := <-running
	if _, err := store.Pool().Exec(ctx, `UPDATE agents.agent_runs SET started_at=now()-interval '2 hours' WHERE id=$1 AND org_id=$2`, run.ID, org); err != nil {
		t.Fatal(err)
	}
	_, _ = agents.RecoverOrphanedRuns(context.Background(), store, nil, 0)
	saved, err := store.GetRun(ctx, run.ID)
	if err != nil || saved.State != "failed" || !saved.WorkReleasePending || saved.ExecutionFinished {
		t.Fatalf("elapsed time invented worker death: %+v %v", saved, err)
	}
	if saved.GrantCleanupPending {
		t.Fatal("grant fence was not attempted")
	}
	if _, err = store.PurgeRuns(ctx, &repo); err == nil {
		t.Fatal("purge erased unknown execution obligation")
	}
}

func TestPurgeFencesAdmissionDuringRemoteCleanup(t *testing.T) {
	org, repo := uuid.New(), uuid.New()
	srv, store := newGRPCServer(t, &stubWorkClient{})
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	grant, err := srv.Grants.Issue(ctx, capability.Grant{OrgID: org, SubjectID: agent.ID, SubjectKind: "agent", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateRun(ctx, agents.Run{OrgID: org, RepoID: repo, AgentID: agent.ID, SponsorID: uuid.New(), GrantID: grant.ID}); err != nil {
		t.Fatal(err)
	}
	paused, release := make(chan struct{}), make(chan struct{})
	revoke := store.GrantRevoker
	store.GrantRevoker = func(call context.Context, id uuid.UUID) error {
		close(paused)
		select {
		case <-call.Done():
			return call.Err()
		case <-release:
			return revoke(call, id)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := store.PurgeRuns(ctx, &repo); done <- err }()
	<-paused
	_, admitErr := store.CreateRun(ctx, agents.Run{OrgID: org, RepoID: repo, AgentID: agent.ID, SponsorID: uuid.New()})
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if admitErr == nil {
		t.Fatal("admission slipped between purge cancellation and final deletion check")
	}
	if _, err = srv.Grants.(ownerFixture).Resolve(ctx, grant.ID); err == nil {
		t.Fatal("purge left live authority")
	}
}
