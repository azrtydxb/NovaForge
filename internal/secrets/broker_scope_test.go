package secrets_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

func scopedBroker(t *testing.T) (*secrets.Broker, context.Context, uuid.UUID) {
	t.Helper()
	pool := brokerPool(t)
	org := uuid.New()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, "DELETE FROM secrets.secret_leases WHERE org_id=$1", org); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM secrets.secret_values WHERE org_id=$1", org); err != nil {
			t.Error(err)
		}
	})
	return secrets.NewBroker(pool, []byte("scope-test-kek")), scopedCtx(org), org
}

func TestBrokerRequiresOrganizationAndRunScope(t *testing.T) {
	b, ctx, org, _ := dynamicBroker(t, 500*time.Millisecond)
	run := uuid.New()
	if err := b.PutValue(ctx, org, "API_KEY", "staging", "staging-value"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []context.Context{context.Background(), scopedCtx(uuid.New()), scopedCtx(uuid.Nil)} {
		if err := b.PutValue(bad, org, "API_KEY", "staging", "replacement"); err == nil {
			t.Error("unscoped/cross-org write accepted")
		}
		if _, err := b.Issue(bad, run, capability.Grant{OrgID: org}, "API_KEY", time.Minute); err == nil {
			t.Error("unscoped/cross-org issuance accepted")
		}
		if _, err := b.IssueFor(bad, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Minute); err == nil {
			t.Error("unscoped/cross-org environment issuance accepted")
		}
	}
	lease, err := b.Issue(ctx, run, capability.Grant{OrgID: org}, "API_KEY", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []context.Context{context.Background(), ctx, secrets.WithRunID(scopedCtx(uuid.New()), run), secrets.WithRunID(ctx, uuid.New())} {
		if value, err := b.Redeem(bad, lease.Token); err == nil || value != "" {
			t.Error("redemption without matching org and run accepted")
		}
	}
	if _, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); err != nil {
		t.Fatal(err)
	}
}

func TestLeasePinsEnvironmentAtIssue(t *testing.T) {
	b, ctx, org, _ := dynamicBroker(t, 500*time.Millisecond)
	run := uuid.New()
	if err := b.PutValue(ctx, org, "API_KEY", "staging", "staging-value"); err != nil {
		t.Fatal(err)
	}
	lease, err := b.Issue(ctx, run, capability.Grant{OrgID: org}, "API_KEY", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PutValue(ctx, org, "API_KEY", "production", "production-value"); err != nil {
		t.Fatal(err)
	}
	value, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if value != "issued-value" {
		t.Fatal("staging lease substituted production material")
	}
	if lease.Environment != "staging" {
		t.Fatal("issued lease did not record its resolved environment")
	}
}

func TestLeaseLifetimeCannotExceedGrant(t *testing.T) {
	b, ctx, org, f := dynamicBroker(t, 500*time.Millisecond)
	run := uuid.New()
	if err := b.PutValue(ctx, org, "API_KEY", "staging", "value"); err != nil {
		t.Fatal(err)
	}
	for _, ttl := range []time.Duration{0, -time.Second, time.Hour + time.Second} {
		if _, err := b.Issue(ctx, run, capability.Grant{OrgID: org}, "API_KEY", ttl); err == nil {
			t.Errorf("invalid TTL %s accepted", ttl)
		}
	}
	if _, err := b.Issue(ctx, uuid.Nil, capability.Grant{OrgID: org}, "API_KEY", time.Minute); err == nil {
		t.Error("nil run accepted")
	}
	if _, err := b.Issue(ctx, run, capability.Grant{OrgID: org, ExpiresAt: time.Now().Add(-time.Second)}, "API_KEY", time.Minute); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired grant: %v", err)
	}
	expires := time.Now().Add(30 * time.Second)
	lease, err := b.Issue(ctx, run, capability.Grant{OrgID: org, ExpiresAt: expires}, "API_KEY", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.ExpiresAt.After(expires) {
		t.Fatal("lease outlives granting authority")
	}
	if err := b.Revoke(ctx, lease.ID); err != nil {
		t.Fatal(err)
	}
	// This lies strictly between the grant and requested TTL. Removing the
	// grant ceiling must cause a real acceptance failure, not still pass.
	f.mu.Lock()
	f.expires = time.Now().Add(45 * time.Second)
	f.mu.Unlock()
	if _, err := b.Issue(ctx, run, capability.Grant{OrgID: org, ExpiresAt: expires}, "API_KEY", time.Minute); !errors.Is(err, secrets.ErrProviderContract) {
		t.Errorf("provider expiry beyond grant accepted: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.leases) != 0 {
		t.Error("rejected grant-overlong credential not revoked at provider")
	}
}
