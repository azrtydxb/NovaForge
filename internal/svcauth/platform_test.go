package svcauth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// TestPlatformTokenNamesNoOrganization pins the shape of the one credential
// that is not org-scoped. A platform worker (the maintenance sweep) must learn
// which organizations exist before it can act in any of them; it gets a token
// naming no organization, which every org-scoped path refuses, and which a
// platform-worker RPC accepts only for returning ids.
func TestPlatformTokenNamesNoOrganization(t *testing.T) {
	tok, err := svcauth.MintPlatform("s3cret", "maintenance-sweep", time.Minute)
	if err != nil {
		t.Fatalf("MintPlatform: %v", err)
	}
	// Verify is what every org-scoped path uses; it must refuse this token.
	if _, _, err := svcauth.Verify("s3cret", tok); err == nil {
		t.Fatal("a platform token verified as an org-scoped service token")
	}
	svc, err := svcauth.VerifyPlatform("s3cret", tok)
	if err != nil || svc != "maintenance-sweep" {
		t.Fatalf("VerifyPlatform = %q, %v", svc, err)
	}
	// An org-scoped token is not a platform token.
	orgTok, _ := svcauth.Mint("s3cret", "maintenance-sweep", uuid.New(), time.Minute)
	if _, err := svcauth.VerifyPlatform("s3cret", orgTok); err == nil {
		t.Fatal("an org-scoped token was accepted as a platform token")
	}
	if _, err := svcauth.VerifyPlatform("other", tok); err == nil {
		t.Fatal("a platform token signed with another secret was accepted")
	}

	scope := scopeFromToken(t, "s3cret", tok)
	if !scope.IsPlatformWorker() || scope.OrgID != uuid.Nil || scope.PlatformWorker != "maintenance-sweep" {
		t.Fatalf("scope = %+v, want a platform worker with no organization", scope)
	}
	if orgScope := scopeFromToken(t, "s3cret", orgTok); orgScope.IsPlatformWorker() {
		t.Fatalf("an org-scoped token resolved as a platform worker: %+v", orgScope)
	}
	// A person with no organization selected is not a platform worker either.
	if (authz.Scope{ActorKind: "user"}).IsPlatformWorker() {
		t.Fatal("an unscoped person is a platform worker")
	}
}

func TestStreamInterceptorInstallsTheCallerScope(t *testing.T) {
	org := uuid.New()
	tok, _ := svcauth.Mint("s3cret", "reader", org, time.Minute)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok))
	var got authz.Scope
	err := svcauth.StreamServerInterceptor(nil, "s3cret")(nil, fakeStream{ctx: ctx}, &grpc.StreamServerInfo{},
		func(_ any, ss grpc.ServerStream) error {
			s, err := authz.FromContext(ss.Context())
			got = s
			return err
		})
	if err != nil || got.OrgID != org {
		t.Fatalf("stream scope = %+v, %v", got, err)
	}
}

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeStream) Context() context.Context { return f.ctx }
