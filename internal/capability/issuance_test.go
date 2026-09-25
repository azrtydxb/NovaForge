package capability_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
)

func TestIssuanceIntentFencesReplay(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(raw, "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := capability.NewStore(pool)
	org, issuer := uuid.New(), uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: issuer, ActorKind: "user"})
	newIntent := func() capability.IssuanceIntent {
		return capability.IssuanceIntent{IssuerID: issuer, IssuerKind: "user", RunID: uuid.New(), Grant: capability.Grant{ID: uuid.New(), OrgID: org, SubjectID: uuid.New(), SubjectKind: "agent", RepoRead: true, WriteBranch: "agents/NF-1/", ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)}}
	}
	t.Run("identical-only", func(t *testing.T) {
		i := newIntent()
		first, err := store.IssueIntent(ctx, i)
		if err != nil {
			t.Fatal(err)
		}
		again, err := store.IssueIntent(ctx, i)
		if err != nil || first != again {
			t.Fatalf("replay changed grant: %+v %v", again, err)
		}
		changed := i
		changed.Grant.ExpiresAt = changed.Grant.ExpiresAt.Add(time.Minute)
		if _, err = store.IssueIntent(ctx, changed); !errors.Is(err, capability.ErrIssuanceDenied) {
			t.Fatal("expiry extension accepted", err)
		}
		changed = i
		changed.RunID = uuid.New()
		if err = store.CancelIssuance(ctx, changed); !errors.Is(err, capability.ErrIssuanceDenied) {
			t.Fatal("mismatched cancellation accepted", err)
		}
		for _, scope := range []authz.Scope{{OrgID: uuid.New(), ActorID: issuer, ActorKind: "user"}, {OrgID: org, ActorID: uuid.New(), ActorKind: "user"}, {OrgID: org, ActorKind: "service"}} {
			foreign := authz.WithScope(context.Background(), scope)
			if _, err = store.IssueIntent(foreign, i); !errors.Is(err, capability.ErrIssuanceDenied) {
				t.Fatal("foreign replay accepted", err)
			}
			if err = store.CancelIssuance(foreign, i); !errors.Is(err, capability.ErrIssuanceDenied) {
				t.Fatal("foreign cancellation accepted", err)
			}
		}
		if err = store.Revoke(ctx, i.Grant.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = store.IssueIntent(ctx, i); !errors.Is(err, capability.ErrIssuanceDenied) {
			t.Fatal("revoked grant resurrected", err)
		}
	})
	for _, order := range []string{"cancel-first", "issue-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			i := newIntent()
			switch order {
			case "cancel-first":
				if err := store.CancelIssuance(ctx, i); err != nil {
					t.Fatal(err)
				}
			case "issue-first":
				if _, err := store.IssueIntent(ctx, i); err != nil {
					t.Fatal(err)
				}
			case "concurrent":
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); _, _ = store.IssueIntent(ctx, i) }()
				go func() {
					defer wg.Done()
					if err := store.CancelIssuance(ctx, i); err != nil {
						t.Error(err)
					}
				}()
				wg.Wait()
			}
			if err := store.CancelIssuance(ctx, i); err != nil {
				t.Fatal(err)
			} // identical lost-reply retry
			if _, err := store.IssueIntent(ctx, i); !errors.Is(err, capability.ErrIssuanceDenied) {
				t.Fatal("tombstone did not fence delayed issue", err)
			}
			if _, err := store.Resolve(ctx, i.Grant.ID); err == nil {
				t.Fatal("cancelled authority still active")
			}
		})
	}
	t.Run("expired", func(t *testing.T) {
		i := newIntent()
		i.Grant.ExpiresAt = time.Now().Add(-time.Second)
		if _, err := store.IssueIntent(ctx, i); !errors.Is(err, capability.ErrIssuanceDenied) {
			t.Fatal("expired intent issued", err)
		}
	})
}
