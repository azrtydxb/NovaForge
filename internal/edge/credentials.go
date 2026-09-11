package edge

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// credKey carries the caller's raw credential through the request context so
// downstream gRPC calls can present it. It is unexported so nothing outside
// this package can inject one.
type credKey struct{}

// WithCredential returns a context carrying the caller's bearer credential.
func WithCredential(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, credKey{}, token)
}

// CredentialFrom returns the credential carried by ctx, if any.
func CredentialFrom(ctx context.Context) string {
	t, _ := ctx.Value(credKey{}).(string)
	return t
}

type orgKey struct{}

// WithOrgRef returns a context carrying the organization the caller named.
func WithOrgRef(ctx context.Context, org string) context.Context {
	if org == "" {
		return ctx
	}
	return context.WithValue(ctx, orgKey{}, org)
}

// OrgRefFrom returns the organization reference carried by ctx, if any.
func OrgRefFrom(ctx context.Context) string {
	o, _ := ctx.Value(orgKey{}).(string)
	return o
}

// ForwardCredential is a gRPC client interceptor that attaches the caller's
// credential to every outbound call.
//
// Without it the edge authenticates a request and then calls the services
// anonymously, so every service-side authorization check sees no caller and
// refuses. The edge is a gateway, not a trusted principal: it carries the
// caller's identity rather than substituting its own.
func ForwardCredential(
	ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
) error {
	if tok := CredentialFrom(ctx); tok != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	}
	if org := OrgRefFrom(ctx); org != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-novaforge-org", org)
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}
