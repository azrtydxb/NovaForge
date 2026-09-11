package svcauth

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// UnaryServerInterceptor resolves the caller into an org-scoped authz.Scope,
// accepting either a user credential (resolved through identity) or a platform
// service token (verified locally against the shared secret).
//
// It is shared so every service resolves callers the same way. Each service
// having its own copy is how two services end up disagreeing about who is
// allowed in.
func UnaryServerInterceptor(identity identityv1.IdentityServiceClient, hmacSecret string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return handler(ctx, req)
		}
		token := strings.TrimPrefix(first(md, "authorization"), "Bearer ")
		if token == "" {
			return handler(ctx, req)
		}
		org := first(md, "x-novaforge-org")

		if strings.HasPrefix(token, Prefix) {
			_, orgID, err := Verify(hmacSecret, token)
			if err != nil {
				return handler(ctx, req)
			}
			return handler(authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"}), req)
		}

		if identity == nil {
			return handler(ctx, req)
		}
		subj, err := resolveUser(ctx, identity, token, org)
		if err != nil {
			return handler(ctx, req)
		}
		scope := authz.Scope{ActorKind: subj.GetActorKind()}
		if id, err := uuid.Parse(subj.GetUserId()); err == nil {
			scope.ActorID = id
		}
		if id, err := uuid.Parse(subj.GetOrgId()); err == nil {
			scope.OrgID = id
		}
		return handler(authz.WithScope(ctx, scope), req)
	}
}

// resolveUser tries the credential as a token then as a session, mirroring the
// edge: a bearer carries a credential, not specifically one kind of it.
func resolveUser(ctx context.Context, identity identityv1.IdentityServiceClient, token, org string) (*identityv1.Subject, error) {
	if r, err := identity.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: token, Org: org}); err == nil {
		return r.GetSubject(), nil
	}
	r, err := identity.ResolveSession(ctx, &identityv1.ResolveSessionRequest{Token: token, Org: org})
	if err != nil {
		return nil, err
	}
	return r.GetSubject(), nil
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}
