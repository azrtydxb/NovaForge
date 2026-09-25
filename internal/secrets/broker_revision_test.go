package secrets_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

func TestLeaseRejectsValueSubstitution(t *testing.T) {
	b, ctx, org, _ := dynamicBroker(t, time.Minute)
	run := uuid.New()
	if err := b.PutValue(ctx, org, "API_KEY", "staging", "original"); err != nil {
		t.Fatal(err)
	}
	lease, err := b.IssueFor(ctx, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PutValue(ctx, org, "API_KEY", "staging", "substituted-static-credential"); err != nil {
		t.Fatal(err)
	}
	if value, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); err != nil || value != "issued-value" {
		t.Fatal("lease silently substituted changed credential material")
	}
}

func TestConcurrentRedeemRevokeSerializes(t *testing.T) {
	b, ctx, org, _ := dynamicBroker(t, time.Minute)
	if err := b.PutValue(ctx, org, "API_KEY", "staging", "value"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		run := uuid.New()
		lease, err := b.Issue(ctx, run, capability.Grant{OrgID: org}, "API_KEY", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var redeemErr, revokeErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, redeemErr = b.Redeem(secrets.WithRunID(ctx, run), lease.Token) }()
		go func() { defer wg.Done(); <-start; revokeErr = b.Revoke(ctx, lease.ID) }()
		close(start)
		wg.Wait()
		if revokeErr != nil {
			t.Fatalf("exactly one operation must succeed: redeem=%v revoke=%v", redeemErr, revokeErr)
		}
		if _, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); err == nil {
			t.Fatal("lease was reusable after racing redemption/revocation")
		}
	}
}
