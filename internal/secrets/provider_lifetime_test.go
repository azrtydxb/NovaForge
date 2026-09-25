package secrets_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

func TestProtocolFixtureLeaseStartsAtIssuance(t *testing.T) {
	const ttl = 500 * time.Millisecond
	b, ctx, org, _ := dynamicBroker(t, ttl)
	if err := b.PutValue(ctx, org, "API_KEY", "staging", "configured"); err != nil {
		t.Fatal(err)
	}
	// Setup and unrelated authorization probes cannot spend the lifetime of
	// a credential the provider has not yet issued.
	time.Sleep(ttl + 50*time.Millisecond)
	run := uuid.New()
	lease, err := b.Issue(ctx, run, capability.Grant{OrgID: org}, "API_KEY", time.Minute)
	if err != nil {
		t.Fatalf("issue after fixture setup delay: %v", err)
	}
	if value, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); err != nil || value != "issued-value" {
		t.Fatalf("newly issued credential: value=%q err=%v", value, err)
	}
}
