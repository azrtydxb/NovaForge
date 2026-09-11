package identity_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/identity"
	"github.com/redis/go-redis/v9"
)

func redisURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_REDIS_URL")
	if u == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	return u
}

func newSessionStore(t *testing.T) *identity.SessionStore {
	t.Helper()
	opts, err := redis.ParseURL(redisURL(t))
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { client.Close() })
	return identity.NewSessionStore(client)
}

func TestSessionResolve(t *testing.T) {
	store := newSessionStore(t)
	ctx := context.Background()
	userID := uuid.New()

	token, err := store.Create(ctx, userID, time.Minute)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != userID {
		t.Fatalf("want user %v, got %v", userID, got)
	}
}

func TestSessionExpired(t *testing.T) {
	store := newSessionStore(t)
	ctx := context.Background()
	userID := uuid.New()

	token, err := store.Create(ctx, userID, time.Millisecond)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	if _, err := store.Resolve(ctx, token); err == nil {
		t.Fatal("want error for expired session")
	}
}
