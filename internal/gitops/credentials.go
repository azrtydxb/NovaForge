package gitops

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// This file is git-platform's credential adapter: how a credential presented
// to a git transport becomes a scope, and how a scope's push is authorized.
// It lived in cmd/git-platform, where no test could reach it, so every
// transport test injected its own AuthFunc and CapFunc — and the functions
// that actually decide who may clone and push on the cluster were never run
// by any test at all.

// ResolveSubject resolves a person's credential — a personal access token or a
// session token, which a transport cannot tell apart — within orgRef.
func ResolveSubject(ctx context.Context, identity identityv1.IdentityServiceClient, credential, orgRef string) (*identityv1.Subject, error) {
	if resp, err := identity.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: credential, Org: orgRef}); err == nil {
		return resp.GetSubject(), nil
	}
	resp, err := identity.ResolveSession(ctx, &identityv1.ResolveSessionRequest{Token: credential, Org: orgRef})
	if err != nil {
		return nil, err
	}
	return resp.GetSubject(), nil
}

// SubjectToScope converts an identity subject into an authz.Scope. A subject
// resolved with no organization yields uuid.Nil, which every org-scoped path
// refuses.
func SubjectToScope(subject *identityv1.Subject) authz.Scope {
	var scope authz.Scope
	scope.ActorID, _ = uuid.Parse(subject.GetUserId())
	scope.OrgID, _ = uuid.Parse(subject.GetOrgId())
	scope.ActorKind = subject.GetActorKind()
	scope.Role = subject.GetRole()
	if scope.ActorKind == "" {
		scope.ActorKind = "user"
	}
	return scope
}

// NewCredentialAuthFunc is the smart-HTTP AuthFunc. The HTTP Basic password
// carries the credential; the username is ignored, as git hosts using token
// auth conventionally do, and is never trusted.
//
// A platform credential (a CI job's clone token, an agent run's credential) is
// verified locally and carries its own organization; anything else is a
// person's credential and is resolved through identity, which checks
// membership of the organization named in the URL.
func NewCredentialAuthFunc(identity identityv1.IdentityServiceClient, hmacSecret string) AuthFunc {
	return func(ctx context.Context, _, pass, orgRef string) (authz.Scope, error) {
		if strings.HasPrefix(pass, svcauth.Prefix) {
			scope, err := svcauth.ScopeFromToken(hmacSecret, pass)
			if err != nil {
				return authz.Scope{}, fmt.Errorf("service token: %w", err)
			}
			return scope, nil
		}
		subject, err := ResolveSubject(ctx, identity, pass, orgRef)
		if err != nil {
			return authz.Scope{}, fmt.Errorf("resolve credential: %w", err)
		}
		return SubjectToScope(subject), nil
	}
}

// NewFingerprintFunc is the SSH transport's key lookup, through identity.
func NewFingerprintFunc(identity identityv1.IdentityServiceClient) FingerprintFunc {
	return func(ctx context.Context, fingerprint, orgRef string) (authz.Scope, error) {
		resp, err := identity.ResolveFingerprint(ctx,
			&identityv1.ResolveFingerprintRequest{Fingerprint: fingerprint, Org: orgRef})
		if err != nil {
			return authz.Scope{}, fmt.Errorf("resolve fingerprint: %w", err)
		}
		return SubjectToScope(resp.GetSubject()), nil
	}
}

// NewAgentPasswordFunc is the SSH transport's password check, and it accepts
// exactly one kind of password: an agent run's platform-signed credential.
//
// An agent holds no SSH key — it is not a person and identity registers none
// for it — so without this the SSH half of "an agent is refused outside its
// grant identically on both transports" could not even be attempted. A
// person's password, token or session is refused here: people authenticate to
// SSH with a key, and accepting their credentials as passwords would open a
// guessing surface the transport never had.
func NewAgentPasswordFunc(hmacSecret string) PasswordFunc {
	return func(_ context.Context, password string) (authz.Scope, error) {
		if !strings.HasPrefix(password, svcauth.Prefix) {
			return authz.Scope{}, fmt.Errorf("password authentication is only for agent credentials")
		}
		scope, err := svcauth.ScopeFromToken(hmacSecret, password)
		if err != nil {
			return authz.Scope{}, err
		}
		if scope.ActorKind != "agent" || scope.ActorID == uuid.Nil {
			return authz.Scope{}, fmt.Errorf("password authentication is only for agent credentials")
		}
		return scope, nil
	}
}

// GrantLister is the part of capability.Store a CapFunc reads.
type GrantLister interface {
	ListActive(ctx context.Context, orgID, subjectID uuid.UUID) ([]capability.Grant, error)
}

// NewGrantCapFunc authorizes ref updates by who is pushing.
//
// Capability grants exist to constrain agents: section 7 of the design is about
// never handing an agent a broad credential. A human member has ordinary write
// access to their organization's repositories — membership was verified when
// their credential was resolved — and demanding a grant of them was a defect.
// Every other caller must hold an active grant covering every ref it updates;
// a platform worker names no actor and so holds none, which is correct: no
// platform worker pushes over a transport.
func NewGrantCapFunc(grants GrantLister) CapFunc {
	return func(ctx context.Context, s authz.Scope, orgID uuid.UUID, _ string, refs []string) error {
		if len(refs) == 0 {
			return nil
		}
		if s.ActorKind == "user" {
			return nil
		}
		return requireGrants(ctx, grants, s, orgID, refs)
	}
}

func requireGrants(ctx context.Context, grants GrantLister, s authz.Scope, orgID uuid.UUID, refs []string) error {
	if s.ActorID == uuid.Nil {
		return fmt.Errorf("write to %s not permitted: a %s caller holds no capability grant", refs[0], s.ActorKind)
	}
	active, err := grants.ListActive(ctx, orgID, s.ActorID)
	if err != nil {
		return fmt.Errorf("resolve capability grants: %w", err)
	}
	for _, ref := range refs {
		permitted := false
		var last error
		for _, g := range active {
			if last = capability.CanWriteRef(g, ref); last == nil {
				permitted = true
				break
			}
		}
		if !permitted {
			if last != nil {
				return last
			}
			return fmt.Errorf("write to %s not permitted: no active grant for %s %s", ref, s.ActorKind, s.ActorID)
		}
	}
	return nil
}
