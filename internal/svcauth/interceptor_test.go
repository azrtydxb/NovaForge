package svcauth_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// TestForwardIncomingCredentialCarriesIdentity pins the defect that a
// service which authenticates a caller and then calls its peers
// anonymously is a service whose peers refuse everything. The edge hit it
// once; agent-runtime hit it again one hop further in, where StartRun's
// lookup of the work item was denied for having no scope.
func TestForwardIncomingCredentialCarriesIdentity(t *testing.T) {
	in := metadata.Pairs(
		"authorization", "Bearer nf.session.abc",
		"x-novaforge-org", "acme",
		"x-something-else", "must not travel",
	)
	ctx := metadata.NewIncomingContext(context.Background(), in)

	var seen metadata.MD
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		seen, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}
	if err := svcauth.ForwardIncomingCredential(ctx, "/x/Y", nil, nil, nil, invoker); err != nil {
		t.Fatalf("ForwardIncomingCredential: %v", err)
	}

	if got := seen.Get("authorization"); len(got) != 1 || got[0] != "Bearer nf.session.abc" {
		t.Fatalf("the credential did not travel: %v", got)
	}
	if got := seen.Get("x-novaforge-org"); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("the organization did not travel: %v", got)
	}
	// Forwarding the whole metadata set would smuggle whatever else a client
	// attached into a service that never asked for it.
	if got := seen.Get("x-something-else"); len(got) != 0 {
		t.Fatalf("an unrelated header was forwarded: %v", got)
	}
}

// TestForwardIncomingCredentialWithNoCaller pins that a call made with no
// incoming credential simply carries none, rather than failing: the
// detached agent-run path attaches its own service token instead.
func TestForwardIncomingCredentialWithNoCaller(t *testing.T) {
	var seen metadata.MD
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		seen, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}
	if err := svcauth.ForwardIncomingCredential(context.Background(), "/x/Y", nil, nil, nil, invoker); err != nil {
		t.Fatalf("ForwardIncomingCredential: %v", err)
	}
	if len(seen.Get("authorization")) != 0 {
		t.Fatalf("a credential appeared from nowhere: %v", seen)
	}
}

// TestAgentRunTokenIsAnAgentNotAService pins a mis-attribution found by
// reading a Work Item in the browser: an agent's own comment appeared as a
// person's.
//
// An agent run presents a service token because it outlives the request that
// started it, and everything the platform records is attributed by actor
// kind. Classifying the run as the platform made the one distinction the
// field exists to make — which of the two wrote this line — wrong.
func TestAgentRunTokenIsAnAgentNotAService(t *testing.T) {
	const secret = "test-secret"
	orgID := uuid.New()

	tok, err := svcauth.Mint(secret, svcauth.AgentRunService, orgID, svcauth.DefaultTTL)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	scope := scopeFromToken(t, secret, tok)
	if scope.ActorKind != "agent" {
		t.Fatalf("an agent run resolved as %q, want agent", scope.ActorKind)
	}
	if scope.OrgID != orgID {
		t.Fatalf("scope org = %s, want %s", scope.OrgID, orgID)
	}

	// Every other platform worker is still the platform.
	other, err := svcauth.Mint(secret, "ci-scheduler", orgID, svcauth.DefaultTTL)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if got := scopeFromToken(t, secret, other).ActorKind; got != "service" {
		t.Fatalf("the CI scheduler resolved as %q, want service", got)
	}
}

// scopeFromToken runs the interceptor with token presented and returns the
// scope the handler was given.
func scopeFromToken(t *testing.T, secret, token string) authz.Scope {
	t.Helper()
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+token))

	var got authz.Scope
	interceptor := svcauth.UnaryServerInterceptor(nil, secret)
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			scope, err := authz.FromContext(ctx)
			if err != nil {
				return nil, err
			}
			got = scope
			return nil, nil
		})
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	return got
}
