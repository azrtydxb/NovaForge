package ci_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestCredentialPartialFailureFencesOnlyAttempt(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	job := uuid.New()
	s.putDynamicSecret("DEPLOY_TOKEN", "staging", "issued-first")
	request := ci.JobCredentials{OrgID: s.org, JobID: job, RepoID: s.repo, Ref: "refs/heads/main", Environment: "staging", Secrets: []string{"DEPLOY_TOKEN", "MISSING_SECOND"}}
	values, err := ci.ResolveJobCredentials(ctx, s.pump.Credentials, request)
	if err == nil || values != nil {
		t.Fatal("partial credentials delivered")
	}
	s.provider.mu.Lock()
	live := len(s.provider.leases)
	s.provider.mu.Unlock()
	if live != 0 {
		t.Fatal("partial batch left first provider credential live")
	}
	s.putDynamicSecret("MISSING_SECOND", "staging", "issued-second")
	values, err = ci.ResolveJobCredentials(ctx, s.pump.Credentials, request)
	if err != nil || len(values) != 2 {
		t.Fatalf("new attempt blocked by previous failure: %v", err)
	}
	result, err := s.pump.Credentials.RevokeJobLeases(ctx, s.org, job, uuid.Nil)
	if err != nil || !result.Fenced {
		t.Fatalf("terminal fence: %+v %v", result, err)
	}
	if _, err := ci.ResolveJobCredentials(ctx, s.pump.Credentials, request); !errors.Is(err, ci.ErrCredentialsDenied) {
		t.Fatalf("terminal job issued again: %v", err)
	}
}

func TestCredentialLateProviderReplyCannotEscapeTerminalFence(t *testing.T) {
	s := newCredentialStack(t)
	s.putDynamicSecret("DEPLOY_TOKEN", "staging", "issued-value")
	s.provider.entered = make(chan struct{})
	s.provider.release = make(chan struct{})
	job := uuid.New()
	done := make(chan error, 1)
	go func() {
		_, err := ci.ResolveJobCredentials(context.Background(), s.pump.Credentials, ci.JobCredentials{OrgID: s.org, JobID: job, RepoID: s.repo, Ref: "refs/heads/main", Environment: "staging", Secrets: []string{"DEPLOY_TOKEN"}})
		done <- err
	}()
	select {
	case <-s.provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider not called")
	}
	result, err := s.pump.Credentials.RevokeJobLeases(context.Background(), s.org, job, uuid.Nil)
	if err != nil || !result.Fenced || result.Pending != 1 {
		close(s.provider.release)
		t.Fatalf("late issuance not pending: %+v %v", result, err)
	}
	close(s.provider.release)
	if err := <-done; err == nil {
		t.Fatal("late credential escaped terminal fence")
	}
	s.provider.mu.Lock()
	defer s.provider.mu.Unlock()
	if len(s.provider.leases) != 0 {
		t.Fatal("late credential not revoked at provider")
	}
}

func TestCredentialRPCBindsOrgJobAndAttempt(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	s.putDynamicSecret("DEPLOY_TOKEN", "staging", "issued-value")
	request := ci.LeaseRequest{OrgID: s.org, JobID: uuid.New(), RepoID: s.repo, Ref: "refs/heads/main", Name: "DEPLOY_TOKEN", Environment: "staging", TTL: 2 * time.Minute}
	if _, err := s.pump.Credentials.IssueJobLease(ctx, request); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing/nonzero attempt requirement: %v", err)
	}
	request.AttemptID = uuid.New()
	lease, err := s.pump.Credentials.IssueJobLease(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pump.Credentials.RedeemJobLease(ctx, uuid.New(), request.JobID, lease.Token); err == nil {
		t.Fatal("cross-org redemption succeeded")
	}
	if _, err := s.pump.Credentials.RedeemJobLease(ctx, s.org, uuid.New(), lease.Token); err == nil {
		t.Fatal("cross-job redemption succeeded")
	}
	if _, err := s.pump.Credentials.RevokeJobLeases(ctx, uuid.New(), request.JobID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pump.Credentials.RedeemJobLease(ctx, s.org, request.JobID, lease.Token); err != nil {
		t.Fatalf("cross-org fence affected owning job: %v", err)
	}
}

func TestCredentialRPCRejectsOtherServiceIdentities(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	s.putDynamicSecret("DEPLOY_TOKEN", "staging", "issued-value")
	request := ci.LeaseRequest{OrgID: s.org, JobID: uuid.New(), RepoID: s.repo, Ref: "refs/heads/main", Name: "DEPLOY_TOKEN", Environment: "staging", TTL: 2 * time.Minute, AttemptID: uuid.New()}
	lease, err := s.pump.Credentials.IssueJobLease(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	client := s.pump.Credentials.(*ci.GatesBroker).Gates
	other, _ := svcauth.Mint(stackHMAC, "unrelated-service", s.org, time.Minute)
	platform, _ := svcauth.MintPlatform(stackHMAC, "ci-credentials", time.Minute)
	agent, _ := svcauth.MintAgentRun(stackHMAC, s.org, uuid.New(), time.Minute)
	expired, _ := svcauth.Mint(stackHMAC, "ci-credentials", s.org, -time.Second)
	valid, _ := svcauth.Mint(stackHMAC, "ci-credentials", s.org, time.Minute)
	for name, token := range map[string]string{"other-service": other, "platform": platform, "agent": agent, "expired": expired, "tampered": valid + "x"} {
		t.Run(name, func(t *testing.T) {
			bad := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token, "x-novaforge-org", s.org.String()))
			if _, err := client.IssueJobLease(bad, &gatesv1.IssueJobLeaseRequest{JobId: request.JobID.String(), RepoId: s.repo.String(), AttemptId: uuid.NewString(), Name: "DEPLOY_TOKEN", Environment: "staging"}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("unapproved issuer: %v", err)
			}
			if _, err := client.RedeemLease(bad, &gatesv1.RedeemLeaseRequest{RunId: request.JobID.String(), Token: lease.Token}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("unapproved redeemer: %v", err)
			}
			if _, err := client.RevokeRunLeases(bad, &gatesv1.RevokeRunLeasesRequest{RunId: request.JobID.String()}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("unapproved lifecycle closer: %v", err)
			}
		})
	}
	if _, err := s.pump.Credentials.RedeemJobLease(ctx, s.org, request.JobID, lease.Token); err != nil {
		t.Fatalf("unauthorized attempt consumed token: %v", err)
	}
}
