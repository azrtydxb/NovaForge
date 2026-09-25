package secrets_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

func TestClosedCredentialRunCannotIssue(t *testing.T) {
	b, ctx, org, f := dynamicBroker(t, time.Minute)
	run := uuid.New()
	result, err := b.RevokeRunLeases(ctx, run, uuid.Nil)
	if err != nil || !result.Fenced || result.Pending != 0 {
		t.Fatalf("close: %+v %v", result, err)
	}
	_, err = b.IssueFor(ctx, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour)
	if !errors.Is(err, secrets.ErrScopeClosed) {
		t.Fatalf("closed run issued: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 0 {
		t.Fatal("closed run contacted issuer")
	}
}

func TestCloseDuringProviderIssuanceRevokesLateReply(t *testing.T) {
	b, ctx, org, f := dynamicBroker(t, time.Minute)
	run := uuid.New()
	f.entered = make(chan struct{})
	f.release = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := b.IssueFor(ctx, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour)
		done <- err
	}()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("issuer not entered")
	}
	result, _ := b.RevokeRunLeases(ctx, run, uuid.Nil)
	if !result.Fenced || result.Pending != 1 {
		close(f.release)
		t.Fatalf("in-flight issuance falsely cleaned: %+v", result)
	}
	close(f.release)
	if err := <-done; err == nil {
		t.Fatal("late provider reply released a credential after run closed")
	}
	result, err := b.RevokeRunLeases(ctx, run, uuid.Nil)
	if err != nil || result.Pending != 0 {
		t.Fatalf("late reply cleanup: %+v %v", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.leases) != 0 {
		t.Fatal("late provider credential was not revoked")
	}
}

func TestFailedProviderRevocationRemainsPendingAndRetries(t *testing.T) {
	b, ctx, org, f := dynamicBroker(t, time.Minute)
	run := uuid.New()
	lease, err := b.IssueFor(ctx, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.revokeFails = true
	f.mu.Unlock()
	result, err := b.RevokeRunLeases(ctx, run, uuid.Nil)
	if !result.Fenced || result.Pending != 1 || !errors.Is(err, secrets.ErrProviderUnavailable) {
		t.Fatalf("failed revoke reported complete: %+v %v", result, err)
	}
	rows, err := b.ListLeases(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State == "revoked" {
		t.Fatal("failed provider revoke marked revoked")
	}
	f.mu.Lock()
	f.revokeFails = false
	f.mu.Unlock()
	if err := b.RetryRevocations(ctx); err != nil {
		t.Fatal(err)
	}
	result, err = b.RevokeRunLeases(ctx, run, uuid.Nil)
	if err != nil || result.Pending != 0 {
		t.Fatalf("retry: %+v %v", result, err)
	}
}

func TestFailedAttemptAllowsFreshAttemptButCannotResume(t *testing.T) {
	b, ctx, org, _ := dynamicBroker(t, time.Minute)
	run, first, second := uuid.New(), uuid.New(), uuid.New()
	lease, err := b.IssueFor(secrets.WithAttemptID(ctx, first), run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.RevokeRunLeases(ctx, run, first)
	if err != nil || !result.Fenced || result.Pending != 0 {
		t.Fatalf("attempt close: %+v %v", result, err)
	}
	if _, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); err == nil {
		t.Fatal("closed attempt remained redeemable")
	}
	if _, err := b.IssueFor(secrets.WithAttemptID(ctx, first), run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour); !errors.Is(err, secrets.ErrScopeClosed) {
		t.Fatalf("closed attempt resumed: %v", err)
	}
	if _, err := b.IssueFor(secrets.WithAttemptID(ctx, second), run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour); err != nil {
		t.Fatalf("legitimate new attempt blocked: %v", err)
	}
	if _, err := b.RevokeRunLeases(context.Background(), run, uuid.Nil); err == nil {
		t.Fatal("unscoped close allowed")
	}
}

func TestRejectsStaticAndOverlongProviderResponses(t *testing.T) {
	for _, mode := range []string{"static", "zero-duration", "overlong"} {
		t.Run(mode, func(t *testing.T) {
			b, ctx, org, f := dynamicBroker(t, time.Minute)
			switch mode {
			case "static":
				f.static = true
			case "zero-duration":
				f.duration = 0
			case "overlong":
				f.expires = time.Now().Add(2 * time.Hour)
			}
			if _, err := b.IssueFor(ctx, uuid.New(), capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour); !errors.Is(err, secrets.ErrProviderContract) {
				t.Fatalf("provider contract accepted: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.leases) != 0 {
				t.Fatal("rejected response left provider credential live")
			}
		})
	}
}

func TestRedeemRechecksExpiryAfterWaitingForLock(t *testing.T) {
	b, ctx, org, _ := dynamicBroker(t, 300*time.Millisecond)
	run := uuid.New()
	lease, err := b.IssueFor(ctx, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pool := brokerPool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT id FROM secrets.secret_leases WHERE id=$1 AND org_id=$2 FOR UPDATE", lease.ID, org); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); done <- err }()
	time.Sleep(time.Until(lease.ExpiresAt) + 30*time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("credential expired while waiting but redeemed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("redemption stuck")
	}
}
