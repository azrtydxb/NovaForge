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

// TestInterceptorAdmitsThePlatformWorker pins the seam between the maintenance
// sweep's platform token and the interceptor every service shares, git-platform
// included: an interceptor that did not understand the token would leave the
// sweep with no scope, the organization list would refuse it, and the sweep
// would cover nothing while reporting only a log line. An org-scoped token and
// an agent run's token keep their own meaning.
func TestInterceptorAdmitsThePlatformWorker(t *testing.T) {
	const secret = "platform-seam-secret"
	intercept := svcauth.UnaryServerInterceptor(nil, secret)
	scopeFor := func(tok string) (authz.Scope, bool) {
		t.Helper()
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok))
		var got authz.Scope
		var ok bool
		_, _ = intercept(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
			s, err := authz.FromContext(ctx)
			got, ok = s, err == nil
			return nil, nil
		})
		return got, ok
	}

	tok, err := svcauth.MintPlatform(secret, "maintenance-sweep", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if scope, ok := scopeFor(tok); !ok || !scope.IsPlatformWorker() || scope.OrgID != uuid.Nil {
		t.Fatalf("platform token resolved to %+v (ok=%v), want a platform worker with no organization", scope, ok)
	}

	org := uuid.New()
	orgTok, _ := svcauth.Mint(secret, "ci-pump", org, time.Minute)
	if scope, ok := scopeFor(orgTok); !ok || scope.IsPlatformWorker() || scope.OrgID != org || scope.ActorKind != "service" {
		t.Fatalf("org-scoped token resolved to %+v", scope)
	}

	agent := uuid.New()
	runTok, _ := svcauth.MintAgentRun(secret, org, agent, time.Minute)
	if scope, ok := scopeFor(runTok); !ok || scope.ActorKind != "agent" || scope.ActorID != agent || scope.OrgID != org {
		t.Fatalf("agent run token resolved to %+v, want agent %s in %s", scope, agent, org)
	}
}
