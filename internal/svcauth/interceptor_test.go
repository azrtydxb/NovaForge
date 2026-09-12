package svcauth_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

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
