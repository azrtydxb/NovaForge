package identity

import (
	"context"
	"errors"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strings"
)

var ErrInvalidCredential = errors.New("invalid or expired credential")
var ErrNotMember = errors.New("not an organization member")

func credentialError(err error) error {
	if errors.Is(err, ErrInvalidCredential) {
		return status.Error(codes.Unauthenticated, "invalid or expired credential")
	}
	return status.Error(codes.Internal, "identity datastore unavailable")
}
func presentedBearer(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) != 1 {
		return ""
	}
	token, ok := strings.CutPrefix(values[0], "Bearer ")
	if !ok {
		return ""
	}
	return token
}
func (s *Server) LogoutSession(ctx context.Context, _ *identityv1.LogoutSessionRequest) (*identityv1.LogoutSessionResponse, error) {
	token := presentedBearer(ctx)
	if token == "" || strings.HasPrefix(token, "nf_") || strings.HasPrefix(token, "nfsvc.") {
		return nil, status.Error(codes.Unauthenticated, "session credential required")
	}
	if err := s.sessions.RevokePresented(ctx, token); err != nil {
		return nil, credentialError(err)
	}
	return &identityv1.LogoutSessionResponse{Ok: true}, nil
}
