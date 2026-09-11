package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/identity"
)

func newTokenStore(t *testing.T) (*identity.TokenStore, identity.User) {
	t.Helper()
	store := newStore(t)
	ctx := context.Background()
	username := uniqueName("carol")
	user, err := store.CreateUser(ctx, username+"@example.com", username, "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return identity.NewTokenStore(storePool(t)), user
}

func TestTokenResolve(t *testing.T) {
	tokens, user := newTokenStore(t)
	ctx := context.Background()

	plaintext, tok, err := tokens.Create(ctx, user.ID, "ci token", []string{"repo:read"}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tok.UserID != user.ID {
		t.Fatalf("want token user %v, got %v", user.ID, tok.UserID)
	}

	resolved, err := tokens.Resolve(ctx, plaintext)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.UserID != user.ID {
		t.Fatalf("want resolved user %v, got %v", user.ID, resolved.UserID)
	}
}

func TestRevokedTokenRejected(t *testing.T) {
	tokens, user := newTokenStore(t)
	ctx := context.Background()

	plaintext, tok, err := tokens.Create(ctx, user.ID, "ci token", []string{"repo:read"}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := tokens.Revoke(ctx, tok.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, err = tokens.Resolve(ctx, plaintext)
	if err == nil || !contains(err.Error(), "revoked") {
		t.Fatalf("want error containing %q, got %v", "revoked", err)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	tokens, user := newTokenStore(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)

	plaintext, _, err := tokens.Create(ctx, user.ID, "ci token", []string{"repo:read"}, &past)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = tokens.Resolve(ctx, plaintext)
	if err == nil || !contains(err.Error(), "expired") {
		t.Fatalf("want error containing %q, got %v", "expired", err)
	}
}
