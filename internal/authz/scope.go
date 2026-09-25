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
	ActorKind string // "user", "agent", or "service"
	// ServiceName is populated only from a verified org-scoped service token.
	// It is empty for users, agents and platform workers, never a request label.
	ServiceName string

	// Role is a person's membership role in OrgID ("owner", "admin" or
	// "member"), as identity reported it when it verified the membership.
	// It is empty for an agent or a platform service, which hold no
	// membership role — so an owner-only action refuses them by default.
	Role string

	// PlatformWorker names a platform worker acting across organizations
	// (see svcauth.MintPlatform). Such a scope has no OrgID, so every
	// org-scoped query refuses it; only an RPC that checks IsPlatformWorker
	// admits it, and such an RPC returns ids and nothing of an organization's
	// content.
	PlatformWorker string

	// RepoLimited marks a scope that holds no membership in OrgID and may touch
	// only the repositories in Repos — an outside collaborator, granted one
	// repository without being a member of the organization that owns it.
	//
	// The default is inverted for these rather than handled at each call site.
	// Thirty-five files authorize with RequireOrg, which compares the
	// organization and nothing more; a collaborator scope that passed it would
	// reach Work Items, Engineering Runs, CI, the secrets listing and the graph.
	// So RequireOrg means "acting as a member of this organization" and refuses a
	// repository-limited scope, which makes every one of those paths fail closed
	// without being touched. A path that should admit a collaborator says so by
	// calling RequireRepo instead.
	RepoLimited bool
	// Repos is the set this scope may touch, and is meaningful only when
	// RepoLimited. An empty set with RepoLimited set reaches nothing, which is
	// the safe reading of "granted nothing".
	Repos []uuid.UUID
}

// IsPlatformWorker reports whether the scope is a platform worker with no
// organization.
func (s Scope) IsPlatformWorker() bool {
	return s.PlatformWorker != "" && s.ActorKind == "service" && s.OrgID == uuid.Nil
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
	// A repository-limited scope holds no membership, so it is not "acting within
	// this organization" in the sense every caller of this function means. It is
	// refused here so a path that has not considered collaborators cannot admit
	// one by accident; RequireRepo is how a path admits them deliberately.
	if s.RepoLimited {
		return fmt.Errorf("organization access denied: this credential reaches only the repositories it was granted")
	}
	return nil
}

// RequireRepo authorizes acting on one repository in an organization.
//
// A member of the organization may reach any of its repositories, exactly as
// before. A repository-limited scope — an outside collaborator — may reach only
// the repositories it was granted, in the organization that granted them.
func RequireRepo(ctx context.Context, orgID, repoID uuid.UUID) error {
	s, err := FromContext(ctx)
	if err != nil {
		return err
	}
	if s.OrgID != orgID {
		return fmt.Errorf("cross-org access denied: scope org %s, requested %s", s.OrgID, orgID)
	}
	if !s.RepoLimited {
		return nil
	}
	for _, r := range s.Repos {
		if r == repoID {
			return nil
		}
	}
	return fmt.Errorf("repository access denied: this credential was not granted repository %s", repoID)
}
