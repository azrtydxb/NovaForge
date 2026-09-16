package svcauth_test

import (
	"context"
	"testing"
	"time"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type deadlineIdentity struct {
	identityv1.IdentityServiceClient
	t *testing.T
}

func (c deadlineIdentity) ResolveToken(ctx context.Context, _ *identityv1.ResolveTokenRequest, _ ...grpc.CallOption) (*identityv1.ResolveTokenResponse, error) {
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > 5*time.Second {
		c.t.Error("identity lookup has no bounded deadline")
	}
	return &identityv1.ResolveTokenResponse{}, nil
}

// Identity lookup needs a bound even for a long-lived incoming stream. Its
// child context must not be returned to the handler after the lookup cancels it.
func TestIdentityLookupDeadlineDoesNotCancelHandler(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer session"))
	intercept := svcauth.UnaryServerInterceptor(deadlineIdentity{t: t}, "secret")
	_, err := intercept(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
		if ctx.Err() != nil {
			t.Errorf("handler context canceled by identity lookup: %v", ctx.Err())
		}
		if _, ok := ctx.Deadline(); ok {
			t.Error("identity lookup deadline leaked into handler")
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
