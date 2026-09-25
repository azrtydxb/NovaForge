package gates

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/metadata"
)

// WithProofService authenticates only proof publication as gates. Repository,
// Work and review reads continue to use the original caller's authority.
func WithProofService(secret string) ControllerOption {
	return WithProofContext(func(ctx context.Context) (context.Context, error) {
		scope, err := authz.FromContext(ctx)
		if err != nil {
			return nil, err
		}
		if scope.OrgID == uuid.Nil {
			return nil, fmt.Errorf("gate proof requires an organization scope")
		}
		token, err := svcauth.Mint(secret, "gates", scope.OrgID, time.Minute)
		if err != nil {
			return nil, err
		}
		// ForwardIncomingCredential appends incoming credentials on the shared
		// connection. Remove them on this derived context so the proof RPC has
		// exactly one credential, without changing the caller's context.
		incoming, _ := metadata.FromIncomingContext(ctx)
		incoming = incoming.Copy()
		incoming.Delete("authorization")
		incoming.Delete("x-novaforge-org")
		ctx = metadata.NewIncomingContext(ctx, incoming)
		outgoing, _ := metadata.FromOutgoingContext(ctx)
		outgoing = outgoing.Copy()
		outgoing.Set("authorization", "Bearer "+token)
		outgoing.Delete("x-novaforge-org")
		return metadata.NewOutgoingContext(ctx, outgoing), nil
	})
}
