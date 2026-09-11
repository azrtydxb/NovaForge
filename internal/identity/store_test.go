package identity_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/identity"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func storePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "identity", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newStore(t *testing.T) *identity.Store {
	t.Helper()
	return identity.NewStore(storePool(t))
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func TestCreateUserAndLookup(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	username := uniqueName("alice")
	email := username + "@example.com"

	created, err := store.CreateUser(ctx, email, username, "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := store.UserByUsername(ctx, username)
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("want ID %v, got %v", created.ID, got.ID)
	}
}

func TestDuplicateUsernameRejected(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	username := uniqueName("bob")

	if _, err := store.CreateUser(ctx, username+"@example.com", username, "hash"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, err := store.CreateUser(ctx, "other-"+username+"@example.com", username, "hash")
	if err == nil {
		t.Fatal("want error for duplicate username")
	}
	if got := err.Error(); !contains(got, "username") {
		t.Fatalf("want error containing %q, got %q", "username", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
