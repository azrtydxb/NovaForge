package capability

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/metadata"
	"time"
)

// RuntimeClient never borrows a user's session or rebinds a persisted issuer
// into authz. Identity validates the durable run receipt independently.
type RuntimeClient struct {
	Identity   identityv1.IdentityServiceClient
	HMACSecret string
}

func (c RuntimeClient) context(ctx context.Context) (context.Context, context.CancelFunc, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return nil, nil, fmt.Errorf("grant caller organization required")
	}
	token, err := svcauth.Mint(c.HMACSecret, "agent-runtime", scope.OrgID, svcauth.DefaultTTL)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), 5*time.Second)
	return ctx, cancel, nil
}
func (c RuntimeClient) Issue(context.Context, Grant) (Grant, error) {
	return Grant{}, fmt.Errorf("stable run intent required")
}
func (c RuntimeClient) IssueIntent(ctx context.Context, i IssuanceIntent) (Grant, error) {
	ctx, cancel, err := c.context(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer cancel()
	r, err := c.Identity.IssueIntent(ctx, &identityv1.IssueIntentRequest{Intent: IntentProto(i)})
	if err != nil {
		return Grant{}, err
	}
	return GrantFromProto(r.GetGrant())
}
func (c RuntimeClient) CancelIssuance(ctx context.Context, i IssuanceIntent) error {
	ctx, cancel, err := c.context(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	r, err := c.Identity.CancelIssuance(ctx, &identityv1.CancelIssuanceRequest{Intent: IntentProto(i)})
	if err == nil && !r.GetOk() {
		return fmt.Errorf("issuance cancellation unconfirmed")
	}
	return err
}
func (c RuntimeClient) Revoke(ctx context.Context, id uuid.UUID) error {
	ctx, cancel, err := c.context(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	r, err := c.Identity.RevokeGrant(ctx, &identityv1.RevokeGrantRequest{Id: id.String()})
	if err == nil && !r.GetOk() {
		return fmt.Errorf("revocation unconfirmed")
	}
	return err
}
func (c RuntimeClient) Resolve(ctx context.Context, id uuid.UUID) (Grant, error) {
	ctx, cancel, err := c.context(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer cancel()
	r, err := c.Identity.GetGrant(ctx, &identityv1.GetGrantRequest{Id: id.String()})
	if err != nil {
		return Grant{}, err
	}
	return GrantFromProto(r.GetGrant())
}
