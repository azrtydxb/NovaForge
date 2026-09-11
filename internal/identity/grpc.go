package identity

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
)

// defaultSessionTTL is how long a session token issued by Login remains
// valid.
const defaultSessionTTL = 24 * time.Hour

// Server implements identityv1.IdentityServiceServer. It is the sole
// component that reads or writes the identity schema and the capability
// grants issued against it; every other service reaches this data only
// through the gRPC surface this type exposes.
type Server struct {
	identityv1.UnimplementedIdentityServiceServer

	store      *Store
	sessions   *SessionStore
	tokens     *TokenStore
	sshKeys    *SSHKeyStore
	grants     *capability.Store
	sessionTTL time.Duration
}

// NewGRPCServer wires store, sessions, tokens, sshKeys, and grants into an
// identityv1.IdentityServiceServer.
func NewGRPCServer(store *Store, sessions *SessionStore, tokens *TokenStore, sshKeys *SSHKeyStore, grants *capability.Store) *Server {
	return &Server{
		store:      store,
		sessions:   sessions,
		tokens:     tokens,
		sshKeys:    sshKeys,
		grants:     grants,
		sessionTTL: defaultSessionTTL,
	}
}

// Register creates a new user account.
func (s *Server) Register(ctx context.Context, req *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	if req.GetEmail() == "" || req.GetUsername() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "email, username, and password are required")
	}
	hash, err := HashPassword(req.GetPassword())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "hash password: %v", err)
	}
	u, err := s.store.CreateUser(ctx, req.GetEmail(), req.GetUsername(), hash)
	if err != nil {
		if strings.Contains(err.Error(), "already taken") {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "register: %v", err)
	}
	return &identityv1.RegisterResponse{UserId: u.ID.String()}, nil
}

// Login verifies username and password and, when TOTP is enabled on the
// account, a valid totp_code. It always returns codes.Unauthenticated on
// any failure, including a missing or invalid TOTP code, so a client cannot
// distinguish "wrong password" from "unknown user" from timing or error
// shape — except that when TOTP is required and not satisfied, the response
// also carries requires_totp=true so the client knows to prompt for one.
func (s *Server) Login(ctx context.Context, req *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	if req.GetUsername() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "username and password are required")
	}
	u, err := s.store.UserByUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid username or password")
	}
	if !VerifyPassword(u.PasswordHash, req.GetPassword()) {
		return nil, status.Error(codes.Unauthenticated, "invalid username or password")
	}
	if u.TOTPSecret.Valid {
		if req.GetTotpCode() == "" || !ValidateTOTP(u.TOTPSecret.String, req.GetTotpCode(), time.Now()) {
			return &identityv1.LoginResponse{RequiresTotp: true}, status.Error(codes.Unauthenticated, "totp code required")
		}
	}
	token, err := s.sessions.Create(ctx, u.ID, s.sessionTTL)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}
	return &identityv1.LoginResponse{SessionToken: token, UserId: u.ID.String()}, nil
}

// ResolveSession resolves a session token minted by Login to its subject.
func (s *Server) ResolveSession(ctx context.Context, req *identityv1.ResolveSessionRequest) (*identityv1.ResolveSessionResponse, error) {
	if req.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	userID, err := s.sessions.Resolve(ctx, req.GetToken())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired session")
	}
	subj := &identityv1.Subject{UserId: userID.String(), ActorKind: "user"}
	if err := s.attachOrg(ctx, subj, userID, req.GetOrg()); err != nil {
		return nil, err
	}
	return &identityv1.ResolveSessionResponse{Subject: subj}, nil
}

// ResolveToken resolves a personal access token to its subject, rejecting a
// revoked or expired one.
func (s *Server) ResolveToken(ctx context.Context, req *identityv1.ResolveTokenRequest) (*identityv1.ResolveTokenResponse, error) {
	if req.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	t, err := s.tokens.Resolve(ctx, req.GetToken())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid, revoked, or expired token")
	}
	subj := &identityv1.Subject{UserId: t.UserID.String(), ActorKind: "user", Scopes: t.Scopes}
	if err := s.attachOrg(ctx, subj, t.UserID, req.GetOrg()); err != nil {
		return nil, err
	}
	return &identityv1.ResolveTokenResponse{Subject: subj}, nil
}

// attachOrg fills in the subject's organization when the caller named one,
// refusing a non-member. A credential alone says who you are, not which
// organization you are acting in, so the org travels with the request and is
// verified here rather than trusted from it.
func (s *Server) attachOrg(ctx context.Context, subj *identityv1.Subject, userID uuid.UUID, ref string) error {
	if ref == "" {
		return nil
	}
	org, err := s.store.ResolveOrgScope(ctx, userID, ref)
	if err != nil {
		return status.Error(codes.PermissionDenied, err.Error())
	}
	subj.OrgId = org.ID.String()
	return nil
}

// ResolveFingerprint resolves an SSH public key's SHA256 fingerprint to its
// owning subject, for the git SSH transport.
func (s *Server) ResolveFingerprint(ctx context.Context, req *identityv1.ResolveFingerprintRequest) (*identityv1.ResolveFingerprintResponse, error) {
	if req.GetFingerprint() == "" {
		return nil, status.Error(codes.InvalidArgument, "fingerprint is required")
	}
	userID, err := s.sshKeys.UserByFingerprint(ctx, req.GetFingerprint())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "unknown fingerprint")
	}
	subj := &identityv1.Subject{UserId: userID.String(), ActorKind: "user"}
	if err := s.attachOrg(ctx, subj, userID, req.GetOrg()); err != nil {
		return nil, err
	}
	return &identityv1.ResolveFingerprintResponse{Subject: subj}, nil
}

// CreateOrg creates an organization owned by the authenticated caller. The
// caller's identity comes only from the scope a server interceptor resolved
// and attached to ctx (see UnaryAuthInterceptor) — never from the request.
func (s *Server) CreateOrg(ctx context.Context, req *identityv1.CreateOrgRequest) (*identityv1.CreateOrgResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.ActorID == uuid.Nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	o, err := s.store.CreateOrg(ctx, req.GetName(), scope.ActorID)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "create org: %v", err)
	}
	return &identityv1.CreateOrgResponse{Org: &identityv1.Org{Id: o.ID.String(), Name: o.Name}}, nil
}

// AddOrgMember adds a user to an organization. The caller must already be a
// member of that organization; a caller who is not gets
// codes.PermissionDenied rather than being told the org doesn't exist.
func (s *Server) AddOrgMember(ctx context.Context, req *identityv1.AddOrgMemberRequest) (*identityv1.AddOrgMemberResponse, error) {
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid org_id")
	}
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}
	if req.GetRole() == "" {
		return nil, status.Error(codes.InvalidArgument, "role is required")
	}
	if err := s.requireOrgMember(ctx, orgID); err != nil {
		return nil, err
	}
	if err := s.store.AddOrgMember(ctx, orgID, userID, req.GetRole()); err != nil {
		if strings.Contains(err.Error(), "already a member") {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "add org member: %v", err)
	}
	return &identityv1.AddOrgMemberResponse{Ok: true}, nil
}

// IssueGrant issues a scoped capability grant within an organization the
// caller belongs to.
func (s *Server) IssueGrant(ctx context.Context, req *identityv1.IssueGrantRequest) (*identityv1.IssueGrantResponse, error) {
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid org_id")
	}
	subjectID, err := uuid.Parse(req.GetSubjectId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid subject_id")
	}
	if req.GetSubjectKind() != "user" && req.GetSubjectKind() != "agent" {
		return nil, status.Error(codes.InvalidArgument, `subject_kind must be "user" or "agent"`)
	}
	if req.GetTtlSeconds() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "ttl_seconds must be positive")
	}
	if err := s.requireOrgMember(ctx, orgID); err != nil {
		return nil, err
	}
	g := capability.Grant{
		OrgID:         orgID,
		SubjectID:     subjectID,
		SubjectKind:   req.GetSubjectKind(),
		RepoRead:      req.GetRepoRead(),
		WriteBranch:   req.GetWriteBranch(),
		SecretsProd:   req.GetSecretsProd(),
		DeployStaging: req.GetDeployStaging(),
		DeployProd:    req.GetDeployProd(),
		ExpiresAt:     time.Now().Add(time.Duration(req.GetTtlSeconds()) * time.Second),
	}
	issued, err := s.grants.Issue(ctx, g)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issue grant: %v", err)
	}
	return &identityv1.IssueGrantResponse{Grant: toProtoGrant(issued)}, nil
}

// GetGrant looks up a previously issued grant by id. The caller must belong
// to the grant's organization.
func (s *Server) GetGrant(ctx context.Context, req *identityv1.GetGrantRequest) (*identityv1.GetGrantResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid id")
	}
	g, err := s.grants.Resolve(ctx, id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "grant not found or expired")
	}
	if err := s.requireOrgMember(ctx, g.OrgID); err != nil {
		return nil, err
	}
	return &identityv1.GetGrantResponse{Grant: toProtoGrant(g)}, nil
}

// requireOrgMember denies the request with codes.PermissionDenied unless the
// authenticated caller in ctx belongs to orgID. It returns
// codes.Unauthenticated if ctx carries no resolved caller at all.
func (s *Server) requireOrgMember(ctx context.Context, orgID uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.ActorID == uuid.Nil {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	member, err := s.store.IsOrgMember(ctx, orgID, scope.ActorID)
	if err != nil {
		return status.Errorf(codes.Internal, "check org membership: %v", err)
	}
	if !member {
		return status.Error(codes.PermissionDenied, "not a member of this organization")
	}
	return nil
}

func toProtoGrant(g capability.Grant) *identityv1.Grant {
	return &identityv1.Grant{
		Id:            g.ID.String(),
		OrgId:         g.OrgID.String(),
		SubjectId:     g.SubjectID.String(),
		SubjectKind:   g.SubjectKind,
		RepoRead:      g.RepoRead,
		WriteBranch:   g.WriteBranch,
		SecretsProd:   g.SecretsProd,
		DeployStaging: g.DeployStaging,
		DeployProd:    g.DeployProd,
		ExpiresAt:     g.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// UnaryAuthInterceptor resolves the caller from the request's
// "authorization" metadata (a "Bearer <token>" session or personal access
// token) and attaches it to the request context as an authz.Scope, so RPCs
// such as CreateOrg, AddOrgMember, IssueGrant, and GetGrant derive who is
// calling from the resolved scope rather than trusting anything the request
// message claims about the caller. A request with no resolvable caller
// proceeds with no scope in context; RPCs that require authentication reject
// it themselves via authz.FromContext.
func UnaryAuthInterceptor(s *Server) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if scope, ok := s.resolveCallerScope(ctx); ok {
			ctx = authz.WithScope(ctx, scope)
		}
		return handler(ctx, req)
	}
}

func (s *Server) resolveCallerScope(ctx context.Context) (authz.Scope, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return authz.Scope{}, false
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return authz.Scope{}, false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" {
		return authz.Scope{}, false
	}
	if userID, err := s.sessions.Resolve(ctx, token); err == nil {
		return authz.Scope{ActorID: userID, ActorKind: "user"}, true
	}
	if t, err := s.tokens.Resolve(ctx, token); err == nil {
		return authz.Scope{ActorID: t.UserID, ActorKind: "user"}, true
	}
	return authz.Scope{}, false
}
