// Package authz carries the caller's organization scope through a request.
// Organizations are a hard security boundary: every query derives its org
// predicate from the Scope in the context, never from a request body.
package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Scope identifies who is acting and which organization they are acting in.
type Scope struct {
	OrgID     uuid.UUID
	ActorID   uuid.UUID
	ActorKind string // "user" or "agent"

	// Role is a person's membership role in OrgID ("owner", "admin" or
	// "member"), as identity reported it when it verified the membership.
	// It is empty for an agent or a platform service, which hold no
	// membership role — so an owner-only action refuses them by default.
	Role string
}

// IsOrgAdmin reports whether the scope is a person holding the owner or admin
// role in its organization.
func (s Scope) IsOrgAdmin() bool {
	return s.ActorKind == "user" && (s.Role == "owner" || s.Role == "admin")
}

// ctxKey is unexported so no other package can install or forge a Scope by
// colliding on the context key.
type ctxKey struct{}

// ErrNoScope is returned when the context carries no Scope.
var ErrNoScope = errors.New("no authorization scope in context")

// WithScope returns a copy of ctx carrying s.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the Scope in ctx, or ErrNoScope.
func FromContext(ctx context.Context) (Scope, error) {
	s, ok := ctx.Value(ctxKey{}).(Scope)
	if !ok {
		return Scope{}, ErrNoScope
	}
	return s, nil
}

// RequireOrg reports an error unless ctx carries a Scope for orgID. A context
// with no Scope is denied rather than treated as unrestricted.
func RequireOrg(ctx context.Context, orgID uuid.UUID) error {
	s, err := FromContext(ctx)
	if err != nil {
		return err
	}
	if s.OrgID != orgID {
		return fmt.Errorf("cross-org access denied: scope org %s, requested %s", s.OrgID, orgID)
	}
	return nil
}
