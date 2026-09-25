package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/identity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestLogoutSessionPreservesSiblingAndPAT(t *testing.T) {
	pool := storePool(t)
	rdb := newRedisClient(t)
	store := identity.NewStore(pool)
	sessions := identity.NewSessionStore(rdb)
	tokens := identity.NewTokenStore(pool)
	u, err := store.CreateUser(context.Background(), uuid.NewString()+"@example.com", uuid.NewString(), "hash")
	if err != nil {
		t.Fatal(err)
	}
	a, err := sessions.Create(context.Background(), u.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sessions.Create(context.Background(), u.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pat, _, err := tokens.Create(context.Background(), u.ID, "logout", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := identity.NewGRPCServer(store, sessions, tokens, nil, nil)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+a))
	if _, err = srv.LogoutSession(ctx, &identityv1.LogoutSessionRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Resolve(context.Background(), a); err == nil {
		t.Fatal("presented session still valid")
	}
	if _, err = sessions.Resolve(context.Background(), b); err != nil {
		t.Fatal("sibling revoked", err)
	}
	if _, err = tokens.Resolve(context.Background(), pat); err != nil {
		t.Fatal("PAT revoked", err)
	}
	ctx = metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+pat))
	if _, err = srv.LogoutSession(ctx, &identityv1.LogoutSessionRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("PAT logout: %v", err)
	}
	if _, err = tokens.Resolve(context.Background(), pat); err != nil {
		t.Fatal("PAT revoked", err)
	}
}

func TestCredentialDatastoreFailureIsInternal(t *testing.T) {
	pool := storePool(t)
	rdb := newRedisClient(t)
	srv := identity.NewGRPCServer(identity.NewStore(pool), identity.NewSessionStore(rdb), identity.NewTokenStore(pool), nil, nil)
	rdb.Close()
	if _, err := srv.ResolveSession(context.Background(), &identityv1.ResolveSessionRequest{Token: "not-a-token"}); status.Code(err) != codes.Internal {
		t.Fatalf("Redis failure classified as %v", err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer unknown"))
	if _, err := srv.LogoutSession(ctx, &identityv1.LogoutSessionRequest{}); status.Code(err) != codes.Internal {
		t.Fatalf("logout Redis failure classified as %v", err)
	}
	pool.Close()
	if _, err := srv.ResolveToken(context.Background(), &identityv1.ResolveTokenRequest{Token: "nf_unknown"}); status.Code(err) != codes.Internal {
		t.Fatalf("PG failure classified as %v", err)
	}
	if _, err := srv.Login(context.Background(), &identityv1.LoginRequest{Username: "unknown", Password: "unknown"}); status.Code(err) != codes.Internal {
		t.Fatalf("login PG failure classified as %v", err)
	}
}
