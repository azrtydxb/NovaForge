package secrets_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

func TestStaticValuesCannotMasqueradeAsDynamicCredentials(t *testing.T) {
	b, ctx, org := scopedBroker(t)
	if err := b.PutValue(ctx, org, "TOKEN", "staging", "durable-value"); err != nil {
		t.Fatal(err)
	}
	_, err := b.IssueFor(ctx, uuid.New(), capability.Grant{OrgID: org}, "TOKEN", "staging", time.Minute)
	if !errors.Is(err, secrets.ErrProviderNotConfigured) {
		t.Fatalf("want fail-closed dynamic-provider requirement, got %v", err)
	}
}

func TestDynamicCredentialRedeemBindsAuthenticatedActor(t *testing.T) {
	b, ctx, org, _ := dynamicBroker(t, time.Minute)
	run := uuid.New()
	lease, err := b.IssueFor(ctx, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := authz.FromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []authz.Scope{{OrgID: org, ActorID: uuid.New(), ActorKind: "user"}, {OrgID: org, ActorID: scope.ActorID, ActorKind: "agent"}, {OrgID: org, ActorKind: "user"}, {OrgID: org, ActorKind: "service"}} {
		badCtx := secrets.WithRunID(authz.WithScope(context.Background(), bad), run)
		if value, err := b.Redeem(badCtx, lease.Token); err == nil || value != "" {
			t.Error("another actor redeemed token by supplying its run ID")
		}
	}
	if _, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); err != nil {
		t.Fatalf("unauthorized attempt consumed rightful actor's token: %v", err)
	}
}

func TestHardExpiryPolicyFailsClosedWithoutTargetEvidence(t *testing.T) {
	org := uuid.New()
	b, err := secrets.NewOpenBaoBroker(nil, []byte("test-kek"), secrets.OpenBaoConfig{Endpoint: "https://issuer.invalid", TokenFile: "/not-read", Bindings: []secrets.OpenBaoBinding{{OrgID: org, Name: "TOKEN", Environment: "staging", Path: "database/creds/role", Method: "GET", JSON: true, RequireHardExpiry: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.IssueFor(scopedCtx(org), uuid.New(), capability.Grant{OrgID: org}, "TOKEN", "staging", time.Minute); !errors.Is(err, secrets.ErrHardExpiryUnavailable) {
		t.Fatalf("hard target expiry invented: %v", err)
	}
}

func TestLegacyLeaseCannotAdoptRedeemerIdentity(t *testing.T) {
	b, ctx, org, _ := dynamicBroker(t, time.Minute)
	run := uuid.New()
	lease, err := b.IssueFor(ctx, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pool := brokerPool(t)
	if _, err := pool.Exec(ctx, "UPDATE secrets.secret_leases SET actor_kind='',actor_id=$3,service_name='' WHERE org_id=$1 AND id=$2", org, lease.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Redeem(secrets.WithRunID(ctx, run), lease.Token); err == nil {
		t.Fatal("legacy missing actor metadata adopted redeemer")
	}
}

func TestRedeemedStaticCredentialCannotClaimRevocation(t *testing.T) {
	for _, locallyRevoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-locally-revoked", true: "historically-locally-revoked"}[locallyRevoked], func(t *testing.T) {
			b, ctx, org := scopedBroker(t)
			pool := brokerPool(t)
			id := uuid.New()
			run := uuid.New()
			hash := sha256.Sum256([]byte(uuid.NewString()))
			// Upgrade fixture: material was released before provider-backed issuance.
			if _, err := pool.Exec(ctx, `INSERT INTO secrets.secret_leases(id,org_id,run_id,name,token_hash,expires_at,redeemed_count,revoked_at)
 VALUES($1,$2,$3,'TOKEN',$4,$5,1,CASE WHEN $6 THEN now() END)`, id, org, run, hash[:], time.Now().Add(time.Hour), locallyRevoked); err != nil {
				t.Fatal(err)
			}
			if err := b.Revoke(ctx, id); !errors.Is(err, secrets.ErrUnderlyingCredentialNotRevocable) {
				t.Errorf("legacy static revocation falsely claimed: %v", err)
			}
			status, cleanupErr := b.RevokeRunLeases(ctx, run, uuid.Nil)
			if !status.Fenced || status.Pending != 1 || !errors.Is(cleanupErr, secrets.ErrUnderlyingCredentialNotRevocable) {
				t.Errorf("legacy cleanup falsely completed: %+v %v", status, cleanupErr)
			}
			rows, err := b.ListLeases(ctx, org)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].State != "rotation_required" {
				t.Fatalf("legacy underlying material falsely considered revoked: %+v", rows)
			}
		})
	}
}
