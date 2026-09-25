package gates

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/metadata"
)

// GrantResolver keeps grant authority at Identity. Production must not migrate
// or query the capability owner's schema just to broker a credential.
type GrantResolver interface {
	Resolve(context.Context, uuid.UUID) (capability.Grant, error)
}

type IdentityGrants struct {
	Client     identityv1.IdentityServiceClient
	HMACSecret string
}

func (g IdentityGrants) Resolve(ctx context.Context, id uuid.UUID) (capability.Grant, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return capability.Grant{}, errors.New("grant resolution requires organization scope")
	}
	token, err := svcauth.Mint(g.HMACSecret, "gates", scope.OrgID, svcauth.DefaultTTL)
	if err != nil {
		return capability.Grant{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Replace, rather than append to, a caller credential: this is the narrow
	// owner's service resolver; subject matching remains at IssueLease.
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token, "x-novaforge-org", scope.OrgID.String()))
	response, err := g.Client.GetGrant(ctx, &identityv1.GetGrantRequest{Id: id.String()})
	if err != nil {
		return capability.Grant{}, err
	}
	p := response.GetGrant()
	gid, e1 := uuid.Parse(p.GetId())
	org, e2 := uuid.Parse(p.GetOrgId())
	subject, e3 := uuid.Parse(p.GetSubjectId())
	expiry, e4 := time.Parse(time.RFC3339Nano, p.GetExpiresAt())
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || gid != id || org != scope.OrgID || subject == uuid.Nil || (p.GetSubjectKind() != "user" && p.GetSubjectKind() != "agent") || !expiry.After(time.Now()) {
		return capability.Grant{}, errors.New("invalid owner grant response")
	}
	return capability.Grant{ID: gid, OrgID: org, SubjectID: subject, SubjectKind: p.GetSubjectKind(), RepoRead: p.GetRepoRead(), WriteBranch: p.GetWriteBranch(), SecretsProd: p.GetSecretsProd(), DeployStaging: p.GetDeployStaging(), DeployProd: p.GetDeployProd(), ExpiresAt: expiry}, nil
}
