package secrets_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/secrets"
)

func TestPurgeDuringProviderIssuancePreservesCleanup(t *testing.T) {
	for _, organization := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "organization"}[organization], func(t *testing.T) {
			b, ctx, org, f := dynamicBroker(t, time.Minute)
			pool := brokerPool(t)
			for _, schema := range []string{"gates", "approvals"} {
				if err := database.Migrate(dbURL(t), schema, os.DirFS("../"+schema+"/migrations")); err != nil {
					t.Fatal(err)
				}
			}
			run := uuid.New()
			p := gates.Purger{Pool: pool}
			purge := func() error {
				if organization {
					return p.PurgeOrganization(ctx)
				}
				return p.PurgeRuns(ctx, []uuid.UUID{run})
			}
			f.entered = make(chan struct{})
			f.release = make(chan struct{})
			f.revokeFails = true
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
			// Purge must return without waiting on provider I/O, retaining the unknown
			// issuance and its fence even before the provider handle is available.
			purgeErr := purge()
			close(f.release)
			issueErr := <-done
			if purgeErr == nil {
				t.Error("purge claimed completion during unknown issuance")
			}
			if issueErr == nil {
				t.Error("late issuance escaped purge fence")
			}
			rows, err := b.ListLeases(ctx, org)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].State != "revocation_pending" {
				t.Fatalf("pending handle lost: %+v", rows)
			}
			if err := purge(); err == nil {
				t.Error("purge claimed completion while revoke unavailable")
			}
			nextRun := run
			if organization {
				nextRun = uuid.New()
			}
			if _, err := b.IssueFor(ctx, nextRun, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour); !errors.Is(err, secrets.ErrScopeClosed) {
				t.Errorf("purged scope admitted issuance: %v", err)
			}
			f.mu.Lock()
			f.revokeFails = false
			f.entered = nil
			f.mu.Unlock()
			if err := b.RetryRevocations(ctx); err != nil {
				t.Fatal(err)
			}
			if err := purge(); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			live := len(f.leases)
			f.mu.Unlock()
			if live != 0 {
				t.Fatal("purge left external credential live")
			}
			rows, err = b.ListLeases(ctx, org)
			if err != nil || len(rows) != 0 {
				t.Fatalf("completed row retained: %+v %v", rows, err)
			}
			if _, err := b.IssueFor(ctx, nextRun, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour); !errors.Is(err, secrets.ErrScopeClosed) {
				t.Errorf("purge completion lost tombstone: %v", err)
			}
		})
	}
}

func TestMissingReservationCompensatesKnownProviderHandle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "available", true: "unavailable"}[fail], func(t *testing.T) {
			b, ctx, org, f := dynamicBroker(t, time.Minute)
			pool := brokerPool(t)
			run := uuid.New()
			f.entered = make(chan struct{})
			f.release = make(chan struct{})
			f.revokeFails = fail
			done := make(chan error, 1)
			go func() {
				_, err := b.IssueFor(ctx, run, capability.Grant{OrgID: org}, "API_KEY", "staging", time.Hour)
				done <- err
			}()
			<-f.entered
			_, err := pool.Exec(ctx, "DELETE FROM secrets.secret_leases WHERE org_id=$1 AND run_id=$2", org, run)
			close(f.release)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Error("missing reservation returned credential")
			}
			f.mu.Lock()
			live := len(f.leases)
			f.mu.Unlock()
			if !fail && live != 0 {
				t.Error("known provider ID was not directly compensated")
			}
			if fail {
				rows, err := b.ListLeases(ctx, org)
				if err != nil || len(rows) != 1 || rows[0].State != "revocation_pending" {
					t.Fatalf("failed compensation lost durable retry: %+v %v", rows, err)
				}
				// Reconstruct the broker: retry must use durable state, not the
				// failed issuance call's process-local provider handle.
				f.mu.Lock()
				f.revokeFails = false
				f.mu.Unlock()
				restarted, err := secrets.NewOpenBaoBroker(brokerPool(t), []byte("protocol-test-kek"), f.config)
				if err != nil {
					t.Fatal(err)
				}
				if err := restarted.RetryRevocations(ctx); err != nil {
					t.Fatal(err)
				}
				f.mu.Lock()
				live = len(f.leases)
				f.mu.Unlock()
				if live != 0 {
					t.Fatal("compensation retry left credential live")
				}
			}
		})
	}
}

func TestPurgeRejectsUnscopedCaller(t *testing.T) {
	p := gates.Purger{Pool: brokerPool(t)}
	if err := p.PurgeRuns(context.Background(), []uuid.UUID{uuid.New()}); err == nil {
		t.Fatal("anonymous purge")
	}
}

func TestPurgePreservesUnknownAndLegacyRotationAcrossOrganizations(t *testing.T) {
	b, ctx, org := scopedBroker(t)
	pool := brokerPool(t)
	run, foreign := uuid.New(), uuid.New()
	for _, row := range []struct {
		org      uuid.UUID
		endpoint string
		redeemed int
		revoked  bool
	}{
		{org, "", 1, true}, {org, "https://unknown.invalid", 0, false},
		{org, "https://completed.invalid", 1, true}, {foreign, "https://foreign.invalid", 0, false},
	} {
		hash := sha256.Sum256([]byte(uuid.NewString()))
		_, err := pool.Exec(ctx, `INSERT INTO secrets.secret_leases(id,org_id,run_id,name,token_hash,expires_at,provider_endpoint,redeemed_count,revoked_at) VALUES($1,$2,$3,'TOKEN',$4,now(),$5,$6,CASE WHEN $7 THEN now() END)`, uuid.New(), row.org, run, hash[:], row.endpoint, row.redeemed, row.revoked)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM secrets.secret_leases WHERE org_id=$1", foreign) })
	if err := secrets.PurgeCredentials(ctx, pool, []uuid.UUID{run, run}); err == nil {
		t.Error("unresolved purge claimed complete")
	}
	rows, err := b.ListLeases(ctx, org)
	if err != nil || len(rows) != 2 {
		t.Fatalf("incorrect purge accounting: %+v %v", rows, err)
	}
	states := map[string]bool{}
	for _, row := range rows {
		states[row.State] = true
	}
	if !states["rotation_required"] || !states["revocation_pending"] {
		t.Fatalf("unresolved records falsely completed: %v", states)
	}
	var requested bool
	if err := pool.QueryRow(ctx, "SELECT revocation_requested FROM secrets.secret_leases WHERE org_id=$1 AND run_id=$2", foreign, run).Scan(&requested); err != nil || requested {
		t.Fatalf("foreign row purged or modified: requested=%v err=%v", requested, err)
	}
	status, _ := b.RevokeRunLeases(ctx, run, uuid.Nil)
	if !status.Fenced || status.Pending != 2 {
		t.Fatalf("unknown/rotation cleanup count: %+v", status)
	}
}
