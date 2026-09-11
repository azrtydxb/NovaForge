package secrets_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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

func newBroker(t *testing.T) *secrets.Broker {
	t.Helper()
	return secrets.NewBroker(brokerPool(t), []byte("test-kek-not-for-production-use"))
}

func TestLeaseExpires(t *testing.T) {
	b := newBroker(t)
	orgID := uuid.New()
	runID := uuid.New()
	ctx := context.Background()

	if err := b.PutValue(ctx, orgID, "API_KEY", "staging", "shh"); err != nil {
		t.Fatalf("PutValue: %v", err)
	}
	lease, err := b.Issue(ctx, runID, capability.Grant{OrgID: orgID}, "API_KEY", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	_, err = b.Redeem(ctx, lease.Token)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("want error containing 'expired', got %v", err)
	}
}

func TestProductionSecretDeniedToStagingGrant(t *testing.T) {
	b := newBroker(t)
	orgID := uuid.New()
	runID := uuid.New()
	ctx := context.Background()

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
	value, err := b.Redeem(ctx, lease.Token)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if value != "prod-secret" {
		t.Fatalf("want prod-secret, got %q", value)
	}
}

func TestRedeemedTokenIsSingleUse(t *testing.T) {
	b := newBroker(t)
	orgID := uuid.New()
	runID := uuid.New()
	ctx := context.Background()

	if err := b.PutValue(ctx, orgID, "TOKEN", "staging", "v1"); err != nil {
		t.Fatalf("PutValue: %v", err)
	}
	lease, err := b.Issue(ctx, runID, capability.Grant{OrgID: orgID}, "TOKEN", time.Minute)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	value, err := b.Redeem(ctx, lease.Token)
	if err != nil {
		t.Fatalf("Redeem first: %v", err)
	}
	if value != "v1" {
		t.Fatalf("want v1, got %q", value)
	}

	_, err = b.Redeem(ctx, lease.Token)
	if err == nil {
		t.Fatal("want a second redeem of the same token to fail")
	}
}

func TestCrossRunRedeemDenied(t *testing.T) {
	b := newBroker(t)
	orgID := uuid.New()
	runA := uuid.New()
	runB := uuid.New()
	ctx := context.Background()

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
	if value != "v" {
		t.Fatalf("want v, got %q", value)
	}
}
