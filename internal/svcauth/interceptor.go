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
			service, orgID, err := Verify(hmacSecret, token)
			if err != nil {
				return handler(ctx, req)
			}
			return handler(authz.WithScope(ctx, authz.Scope{
				OrgID:     orgID,
				ActorKind: actorKindFor(service),
			}), req)
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

// ForwardIncomingCredential is a gRPC client interceptor that copies the
// caller's credential from the incoming request onto every outbound call a
// handler makes.
//
// A service that authenticates a request and then calls its peers
// anonymously is a service whose peers refuse everything: they see no
// caller and, correctly, deny. The edge hit exactly this and fixed it with
// its own interceptor; a service calling another service on a caller's
// behalf needs the same, and needs it here rather than as a third copy.
//
// Only the two headers that carry identity are forwarded. Copying the whole
// metadata set would forward whatever else a client attached, which is a
// way to smuggle values into a service that never asked for them.
func ForwardIncomingCredential(
	ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
) error {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := first(md, "authorization"); v != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", v)
		}
		if v := first(md, "x-novaforge-org"); v != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "x-novaforge-org", v)
		}
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

// AgentRunService is the service name an agent run mints its token under.
// The platform has one identity that is not the platform acting for itself:
// a run acting on behalf of an agent.
const AgentRunService = "agent-run"

// actorKindFor classifies a service token's holder.
//
// An agent run presents a service token because it outlives the request that
// started it, but it is an agent, not the platform — and things the platform
// records are attributed by actor kind. Calling it a service made an agent's
// own comment on a Work Item appear as a person's, which is exactly the
// distinction the field exists to make.
func actorKindFor(service string) string {
	if service == AgentRunService {
		return "agent"
	}
	return "service"
}
