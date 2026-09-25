package gates

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestProofServiceCredentialIsolation(t *testing.T) {
	org := uuid.New()
	ctx, cancel := context.WithTimeout(authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "user"}), time.Minute)
	defer cancel()
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer human", "x-novaforge-org", "caller-org"))
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer old", "x-novaforge-org", "old-org", "trace-id", "keep"))
	reviews := &sandboxReviews{}
	controller := NewController(nil, nil, reviews, nil, "", WithProofService("test-proof-secret"))
	if err := controller.Proof(ctx, uuid.New(), "tests", "pass", "evidence"); err != nil {
		t.Fatal(err)
	}
	called := false
	err := svcauth.ForwardIncomingCredential(reviews.seen, "/proof", nil, nil, nil,
		func(forwarded context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			called = true
			md, _ := metadata.FromOutgoingContext(forwarded)
			credentials := md.Get("authorization")
			if len(credentials) != 1 {
				t.Fatalf("proof carried %d credentials", len(credentials))
			}
			scope, err := svcauth.ScopeFromToken("test-proof-secret", strings.TrimPrefix(credentials[0], "Bearer "))
			if err != nil || scope.OrgID != org || scope.ServiceName != "gates" || scope.ActorKind != "service" {
				t.Fatalf("incorrect proof authority: %+v %v", scope, err)
			}
			if len(md.Get("x-novaforge-org")) != 0 || len(md.Get("trace-id")) != 1 || md.Get("trace-id")[0] != "keep" {
				t.Fatal("caller org selector leaked or tracing lost")
			}
			deadline, _ := forwarded.Deadline()
			originalDeadline, _ := ctx.Deadline()
			if deadline != originalDeadline {
				t.Fatal("proof escaped caller deadline")
			}
			return nil
		})
	if err != nil || !called {
		t.Fatalf("forwarding not exercised: %v", err)
	}
	incoming, _ := metadata.FromIncomingContext(ctx)
	outgoing, _ := metadata.FromOutgoingContext(ctx)
	if incoming.Get("authorization")[0] != "Bearer human" || outgoing.Get("authorization")[0] != "Bearer old" {
		t.Fatal("proof signer mutated caller authority")
	}
}

func TestProofServiceRejectsMissingAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		ctx          context.Context
	}{
		{"unscoped", "secret", context.Background()},
		{"platform", "secret", authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "gates"})},
		{"missing-secret", "", authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New()})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reviews := &sandboxReviews{}
			controller := NewController(nil, nil, reviews, nil, "", WithProofService(tc.secret))
			if err := controller.Proof(tc.ctx, uuid.New(), "tests", "pass", "evidence"); err == nil || reviews.seen != nil {
				t.Fatal("proof sent without scoped signing authority")
			}
		})
	}
}
