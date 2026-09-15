package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	"github.com/novaforge/novaforge/internal/svcauth"
)

// TestInterceptorAdmitsThePlatformSweep pins the seam between the sweep's
// platform token and git-platform's own interceptor, which is not the shared
// one: an interceptor that did not understand the token would leave the sweep
// with no scope, the organization list would refuse it, and the sweep would
// cover nothing while reporting only a log line.
func TestInterceptorAdmitsThePlatformSweep(t *testing.T) {
	hmacSecret = "platform-seam-secret"
	t.Cleanup(func() { hmacSecret = "" })

	tok, err := svcauth.MintPlatform(hmacSecret, "maintenance-sweep", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok))
	scope, ok := resolveScopeFromMetadata(ctx, nil)
	if !ok || !scope.IsPlatformWorker() {
		t.Fatalf("platform token resolved to %+v (ok=%v), want a platform worker", scope, ok)
	}

	orgTok, _ := svcauth.Mint(hmacSecret, "ci-pump", uuid.New(), time.Minute)
	ctx = metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+orgTok))
	if scope, ok := resolveScopeFromMetadata(ctx, nil); !ok || scope.IsPlatformWorker() || scope.OrgID == uuid.Nil {
		t.Fatalf("org-scoped token resolved to %+v", scope)
	}
}
