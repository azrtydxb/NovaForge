package secrets_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/secrets"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func brokerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "secrets", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestLeaseExpires(t *testing.T) {
	b, ctx, orgID, _ := dynamicBroker(t, 500*time.Millisecond)
	runID := uuid.New()

	if err := b.PutValue(ctx, orgID, "API_KEY", "staging", "shh"); err != nil {
		t.Fatalf("PutValue: %v", err)
	}
	lease, err := b.Issue(ctx, runID, capability.Grant{OrgID: orgID}, "API_KEY", time.Second)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	time.Sleep(600 * time.Millisecond)

	_, err = b.Redeem(secrets.WithRunID(ctx, runID), lease.Token)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("want error containing 'expired', got %v", err)
	}
}

func TestProductionSecretDeniedToStagingGrant(t *testing.T) {
	b, ctx, orgID, _ := dynamicBroker(t, 500*time.Millisecond)
	runID := uuid.New()

	if err := b.PutValue(ctx, orgID, "DEPLOY_KEY", "production", "prod-secret"); err != nil {
		t.Fatalf("PutValue: %v", err)
	}

	_, err := b.Issue(ctx, runID, capability.Grant{OrgID: orgID, SecretsProd: false}, "DEPLOY_KEY", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("want error containing 'not permitted', got %v", err)
	}

	// A grant that is allowed production secrets succeeds.
	lease, err := b.Issue(ctx, runID, capability.Grant{OrgID: orgID, SecretsProd: true}, "DEPLOY_KEY", time.Minute)
	if err != nil {
		t.Fatalf("Issue with SecretsProd grant: %v", err)
	}
	value, err := b.Redeem(secrets.WithRunID(ctx, runID), lease.Token)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if value != "issued-value" {
		t.Fatalf("want prod-secret, got %q", value)
	}
}

func TestRedeemedTokenIsSingleUse(t *testing.T) {
	b, ctx, orgID, _ := dynamicBroker(t, 500*time.Millisecond)
	runID := uuid.New()

	if err := b.PutValue(ctx, orgID, "TOKEN", "staging", "v1"); err != nil {
		t.Fatalf("PutValue: %v", err)
	}
	lease, err := b.Issue(ctx, runID, capability.Grant{OrgID: orgID}, "API_KEY", time.Minute)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	value, err := b.Redeem(secrets.WithRunID(ctx, runID), lease.Token)
	if err != nil {
		t.Fatalf("Redeem first: %v", err)
	}
	if value != "issued-value" {
		t.Fatalf("want v1, got %q", value)
	}

	_, err = b.Redeem(secrets.WithRunID(ctx, runID), lease.Token)
	if err == nil {
		t.Fatal("want a second redeem of the same token to fail")
	}
}

func TestCrossRunRedeemDenied(t *testing.T) {
	b, ctx, orgID, _ := dynamicBroker(t, 500*time.Millisecond)
	runA := uuid.New()
	runB := uuid.New()

	if err := b.PutValue(ctx, orgID, "SECRET", "staging", "v"); err != nil {
		t.Fatalf("PutValue: %v", err)
	}
	lease, err := b.Issue(ctx, runA, capability.Grant{OrgID: orgID}, "SECRET", time.Minute)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = b.Redeem(secrets.WithRunID(ctx, runB), lease.Token)
	if err == nil {
		t.Fatal("want a lease issued to run A to be denied when redeemed in run B's context")
	}

	// The same lease redeemed in its own run's context still works.
	value, err := b.Redeem(secrets.WithRunID(ctx, runA), lease.Token)
	if err != nil {
		t.Fatalf("Redeem in the issuing run's own context: %v", err)
	}
	if value != "issued-value" {
		t.Fatalf("want v, got %q", value)
	}
}

// TestRevokeIsOrgScoped pins a defect of the same class as the CI job claim
// that carried no organization predicate: a lease id is guessable in the way
// any uuid is, and without the predicate one organization could revoke
// another's live credential and stall its runs.
func TestRevokeIsOrgScoped(t *testing.T) {
	broker, ctxA, orgA, _ := dynamicBroker(t, 500*time.Millisecond)
	orgB := uuid.New()
	runID := uuid.New()

	if err := broker.PutValue(ctxA, orgA, "AWS_STAGING", "staging", "s3cret"); err != nil {
		t.Fatalf("PutValue: %v", err)
	}
	lease, err := broker.Issue(ctxA, runID, capability.Grant{OrgID: orgA}, "AWS_STAGING", time.Minute)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := broker.Revoke(scopedCtx(orgB), lease.ID); err == nil {
		t.Fatal("another organization revoked this organization's lease")
	}

	// The lease must still be live afterwards: a refused revocation that
	// nonetheless revoked would be worse than one that succeeded openly.
	leases, err := broker.ListLeases(ctxA, orgA)
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	var found bool
	for _, l := range leases {
		if l.ID == lease.ID {
			found = true
			if l.State != "issued" {
				t.Fatalf("the lease is %q after a refused revocation, want issued", l.State)
			}
		}
	}
	if !found {
		t.Fatal("the lease is not listed")
	}

	if err := broker.Revoke(ctxA, lease.ID); err != nil {
		t.Fatalf("the owning organization could not revoke its own lease: %v", err)
	}
}

// TestListNeverReturnsSecretMaterial pins that listing secrets is safe to
// offer: the query does not select the ciphertext column at all, so there is
// nothing for a caller to decrypt.
func TestListNeverReturnsSecretMaterial(t *testing.T) {
	broker, ctx, orgID := scopedBroker(t)

	if err := broker.PutValue(ctx, orgID, "DEPLOY_KEY", "production", "very-secret-value"); err != nil {
		t.Fatalf("PutValue: %v", err)
	}
	refs, err := broker.List(ctx, orgID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].Name != "DEPLOY_KEY" || refs[0].Environment != "production" {
		t.Fatalf("unexpected listing: %+v", refs)
	}
}

// scopedCtx carries an organization scope, which the listing and revocation
// paths require: they are org-scoped reads and writes, not the unscoped
// broker operations a run performs with a grant in hand.
func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}
