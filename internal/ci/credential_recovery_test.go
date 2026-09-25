package ci_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/ci"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func credentialWorkerContext() context.Context {
	return authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "ci-credentials"})
}

func TestCredentialOutboxRPCFailureRestartDeletedOrg(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	s.putDynamicSecret("DEPLOY_TOKEN", "staging", "private-issued-value")
	job := s.job("refs/heads/main", "outbox", "staging", "DEPLOY_TOKEN")
	attempt := uuid.New()
	_, err := ci.ResolveJobCredentials(ctx, s.pump.Credentials, ci.JobCredentials{AttemptID: attempt, OrgID: s.org, JobID: job, RepoID: s.repo, Ref: "refs/heads/main", Environment: "staging", Secrets: []string{"DEPLOY_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	s.provider.mu.Lock()
	s.provider.revokeFails = true
	s.provider.mu.Unlock()
	if err := s.store.SetJobStatus(ctx, job, "cancelled", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.store.RetryCredentialCleanup(ctx, s.pump.Credentials); err == nil {
		t.Fatal("anonymous discovery permitted")
	}
	if err := s.store.RetryCredentialCleanup(credentialWorkerContext(), s.pump.Credentials); !errors.Is(err, ci.ErrCredentialCleanupPending) {
		t.Fatalf("provider failure falsely acknowledged: %v", err)
	}
	var count int
	countRows := func() {
		t.Helper()
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ci.credential_cleanup WHERE org_id=$1 AND job_id=$2`, s.org, job).Scan(&count); err != nil {
			t.Fatal(err)
		}
	}
	countRows()
	if count != 1 {
		t.Fatal("pending provider work lost")
	}
	// CI can lose every run and runner after deletion; the independent journal
	// must still identify this org without consulting Identity's current list.
	if _, err := s.pool.Exec(ctx, `DELETE FROM ci.workflow_runs WHERE org_id=$1`, s.org); err != nil {
		t.Fatal(err)
	}
	fresh := ci.NewStore(s.pool)
	s.provider.mu.Lock()
	s.provider.revokeFails = false
	s.provider.mu.Unlock()
	if err := fresh.RetryCredentialCleanup(credentialWorkerContext(), s.pump.Credentials); err != nil {
		t.Fatal(err)
	}
	countRows()
	if count != 0 {
		t.Fatal("confirmed cleanup not acknowledged")
	}
	s.provider.mu.Lock()
	live := len(s.provider.leases)
	s.provider.mu.Unlock()
	if live != 0 {
		t.Fatal("external lease remains")
	}
	_, err = s.pump.Credentials.IssueJobLease(ctx, ci.LeaseRequest{OrgID: s.org, JobID: job, RepoID: s.repo, AttemptID: uuid.New(), Ref: "refs/heads/main", Environment: "staging", Name: "DEPLOY_TOKEN", TTL: time.Minute})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("deleted job admitted late issuance: %v", err)
	}
}

func TestCredentialPreparationRecoveryFencesDispatchNotRunningJob(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	for _, phase := range []string{"preparing", "dispatched"} {
		t.Run(phase, func(t *testing.T) {
			job := s.job("refs/heads/main", phase, "staging", "DEPLOY_TOKEN")
			if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil); err != nil {
				t.Fatal(err)
			}
			attempt := uuid.New()
			if _, err := s.pool.Exec(ctx, `INSERT INTO ci.credential_cleanup(org_id,job_id,attempt_id,phase,ready_at) VALUES($1,$2,$3,$4,now()-interval '1 hour')`, s.org, job, attempt, phase); err != nil {
				t.Fatal(err)
			}
			if phase == "dispatched" {
				if err := s.store.StartClaimedJob(ctx, job, s.runnerID); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.store.RecoverCredentialPreparations(credentialWorkerContext()); err != nil {
				t.Fatal(err)
			}
			state := s.jobState(job)
			if phase == "preparing" {
				if state.Status != "failure" {
					t.Fatal("stale preparation kept dispatch authority")
				}
				if err := s.store.StartClaimedJob(ctx, job, s.runnerID, attempt); err == nil {
					t.Fatal("late dispatch escaped recovery")
				}
			} else if state.Status != "running" {
				t.Fatal("timer revoked legitimate running job")
			}
		})
	}
}
