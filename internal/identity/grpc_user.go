package identity

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// caller returns the authenticated actor, refusing an unauthenticated request.
// Every method here derives the subject from the interceptor's scope, never
// from the request message.
func (s *Server) caller(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.ActorID == uuid.Nil {
		return uuid.Nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	return scope.ActorID, nil
}

// GetCurrentUser returns the authenticated user.
func (s *Server) GetCurrentUser(ctx context.Context, _ *identityv1.GetCurrentUserRequest) (*identityv1.GetCurrentUserResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.store.UserByID(ctx, id)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &identityv1.GetCurrentUserResponse{User: &identityv1.User{
		Id: u.ID.String(), Email: u.Email, Username: u.Username,
		TotpEnabled: u.TOTPSecret.Valid && u.TOTPSecret.String != "",
	}}, nil
}

// ListOrgs returns the organizations the caller belongs to.
func (s *Server) ListOrgs(ctx context.Context, _ *identityv1.ListOrgsRequest) (*identityv1.ListOrgsResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	orgs, err := s.store.ListOrgsForUser(ctx, id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*identityv1.Org, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, &identityv1.Org{Id: o.ID.String(), Name: o.Name})
	}
	return &identityv1.ListOrgsResponse{Orgs: out}, nil
}

// GetOrg returns one organization the caller belongs to.
func (s *Server) GetOrg(ctx context.Context, req *identityv1.GetOrgRequest) (*identityv1.GetOrgResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.store.ResolveOrgScope(ctx, id, req.GetOrg())
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	return &identityv1.GetOrgResponse{Org: &identityv1.Org{Id: o.ID.String(), Name: o.Name}}, nil
}

// ListOrgMembers returns an organization's members, for a caller who is one.
func (s *Server) ListOrgMembers(ctx context.Context, req *identityv1.ListOrgMembersRequest) (*identityv1.ListOrgMembersResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.store.ResolveOrgScope(ctx, id, req.GetOrg())
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	members, err := s.store.ListOrgMembers(ctx, o.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*identityv1.OrgMember, 0, len(members))
	for _, m := range members {
		out = append(out, &identityv1.OrgMember{
			UserId: m.UserID.String(), Username: m.Username, Role: m.Role,
		})
	}
	return &identityv1.ListOrgMembersResponse{Members: out}, nil
}

// AddSSHKey registers a public key for the caller.
func (s *Server) AddSSHKey(ctx context.Context, req *identityv1.AddSSHKeyRequest) (*identityv1.AddSSHKeyResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	k, err := s.sshKeys.Add(ctx, id, req.GetTitle(), req.GetKey())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &identityv1.AddSSHKeyResponse{Key: &identityv1.SSHKey{
		Id: k.ID.String(), Title: k.Title, Fingerprint: k.Fingerprint,
	}}, nil
}

// ListSSHKeys returns the caller's registered public keys.
func (s *Server) ListSSHKeys(ctx context.Context, _ *identityv1.ListSSHKeysRequest) (*identityv1.ListSSHKeysResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := s.sshKeys.List(ctx, id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*identityv1.SSHKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, &identityv1.SSHKey{
			Id: k.ID.String(), Title: k.Title, Fingerprint: k.Fingerprint,
		})
	}
	return &identityv1.ListSSHKeysResponse{Keys: out}, nil
}

// DeleteSSHKey removes one of the caller's own keys.
func (s *Server) DeleteSSHKey(ctx context.Context, req *identityv1.DeleteSSHKeyRequest) (*identityv1.DeleteSSHKeyResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	keyID, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid key id")
	}
	if err := s.sshKeys.Delete(ctx, id, keyID); err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &identityv1.DeleteSSHKeyResponse{Ok: true}, nil
}

// CreateToken issues a personal access token. The plaintext is returned here
// and nowhere else, because only its hash is stored.
func (s *Server) CreateToken(ctx context.Context, req *identityv1.CreateTokenRequest) (*identityv1.CreateTokenResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	var expires *time.Time
	if req.GetTtlSeconds() > 0 {
		t := time.Now().Add(time.Duration(req.GetTtlSeconds()) * time.Second)
		expires = &t
	}
	plaintext, tok, err := s.tokens.Create(ctx, id, req.GetName(), req.GetScopes(), expires)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &identityv1.CreateTokenResponse{
		Token:     tokenProto(tok),
		Plaintext: plaintext,
	}, nil
}

// ListTokens returns the caller's active tokens, without their plaintext.
func (s *Server) ListTokens(ctx context.Context, _ *identityv1.ListTokensRequest) (*identityv1.ListTokensResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	toks, err := s.tokens.List(ctx, id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*identityv1.AccessToken, 0, len(toks))
	for _, t := range toks {
		out = append(out, tokenProto(t))
	}
	return &identityv1.ListTokensResponse{Tokens: out}, nil
}

// DeleteToken revokes one of the caller's tokens.
func (s *Server) DeleteToken(ctx context.Context, req *identityv1.DeleteTokenRequest) (*identityv1.DeleteTokenResponse, error) {
	callerID, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	tokID, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid token id")
	}
	// Revoke only the caller's own token: ownership is checked before the
	// revoke, not inferred from the id being unguessable.
	toks, err := s.tokens.List(ctx, callerID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	owned := false
	for _, t := range toks {
		if t.ID == tokID {
			owned = true
			break
		}
	}
	if !owned {
		return nil, status.Error(codes.NotFound, "no such token for this user")
	}
	if err := s.tokens.Revoke(ctx, tokID); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &identityv1.DeleteTokenResponse{Ok: true}, nil
}

// Setup2FA generates a TOTP secret and stashes it as a pending enrolment.
//
// The secret is deliberately NOT written to the user row here: login treats a
// present secret as "two-factor enabled", so storing it before the caller has
// proved they can generate a code would lock them out of their own account.
func (s *Server) Setup2FA(ctx context.Context, _ *identityv1.Setup2FARequest) (*identityv1.Setup2FAResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	secret, uri, err := GenerateTOTPSecret()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.sessions.StashPendingTOTP(ctx, id, secret); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &identityv1.Setup2FAResponse{Secret: secret, Uri: uri}, nil
}

// Verify2FA completes enrolment: the caller proves they can produce a code
// from the pending secret, and only then does it become their second factor.
func (s *Server) Verify2FA(ctx context.Context, req *identityv1.Verify2FARequest) (*identityv1.Verify2FAResponse, error) {
	id, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := s.sessions.PendingTOTP(ctx, id)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if !ValidateTOTP(secret, req.GetCode(), time.Now()) {
		return nil, status.Error(codes.InvalidArgument, "incorrect code")
	}
	if err := s.store.SetTOTPSecret(ctx, id, secret); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.sessions.ClearPendingTOTP(ctx, id); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &identityv1.Verify2FAResponse{Enabled: true}, nil
}

func tokenProto(t Token) *identityv1.AccessToken {
	out := &identityv1.AccessToken{Id: t.ID.String(), Name: t.Name, Scopes: t.Scopes}
	if t.ExpiresAt != nil {
		out.ExpiresAt = t.ExpiresAt.Format(time.RFC3339)
	}
	return out
}
