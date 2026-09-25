package identity

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// teamAdmin resolves the caller and checks they may change this organization's
// teams. A team is part of the organization's access structure, so the same
// owners and admins who change membership change teams; a member who could create
// a team could grant themselves whatever the team grants.
func (s *Server) teamAdmin(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.ActorID == uuid.Nil || scope.OrgID == uuid.Nil {
		return authz.Scope{}, status.Error(codes.Unauthenticated, "authentication required")
	}
	role, err := s.store.MemberRole(ctx, scope.OrgID, scope.ActorID)
	if err != nil {
		return authz.Scope{}, status.Error(codes.PermissionDenied, "not a member of this organization")
	}
	if role != "owner" && role != "admin" {
		return authz.Scope{}, status.Error(codes.PermissionDenied, "only an owner or admin may change teams")
	}
	return scope, nil
}

// teamMember resolves the caller for a read. Any member may see the teams of the
// organization they belong to: who is in which team is not a secret from the people
// it decides access for.
func (s *Server) teamMember(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return authz.Scope{}, status.Error(codes.Unauthenticated, "authentication required")
	}
	return scope, nil
}

func teamProto(t Team, members []uuid.UUID) *identityv1.Team {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.String())
	}
	return &identityv1.Team{
		Id: t.ID.String(), OrgId: t.OrgID.String(),
		Name: t.Name, Role: t.Role, MemberIds: ids,
	}
}

// ListTeams returns the caller's organization's teams with their members.
func (s *Server) ListTeams(ctx context.Context, _ *identityv1.ListTeamsRequest) (*identityv1.ListTeamsResponse, error) {
	scope, err := s.teamMember(ctx)
	if err != nil {
		return nil, err
	}
	teams, err := s.store.ListTeams(ctx, scope.OrgID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list teams: %v", err)
	}
	out := make([]*identityv1.Team, 0, len(teams))
	for _, t := range teams {
		members, err := s.store.ListTeamMembers(ctx, scope.OrgID, t.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list team members: %v", err)
		}
		out = append(out, teamProto(t, members))
	}
	return &identityv1.ListTeamsResponse{Teams: out}, nil
}

// CreateTeam adds a team to the caller's organization.
func (s *Server) CreateTeam(ctx context.Context, req *identityv1.CreateTeamRequest) (*identityv1.CreateTeamResponse, error) {
	scope, err := s.teamAdmin(ctx)
	if err != nil {
		return nil, err
	}
	switch req.GetRole() {
	case "owner", "admin", "member":
	case "":
		return nil, status.Error(codes.InvalidArgument, "role is required")
	default:
		return nil, status.Errorf(codes.InvalidArgument, "role %q is not one of owner, admin or member", req.GetRole())
	}
	team, err := s.store.CreateTeam(ctx, scope.OrgID, req.GetName(), req.GetRole())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "create team: %v", err)
	}
	return &identityv1.CreateTeamResponse{Team: teamProto(team, nil)}, nil
}

// DeleteTeam removes a team and its memberships.
func (s *Server) DeleteTeam(ctx context.Context, req *identityv1.DeleteTeamRequest) (*identityv1.DeleteTeamResponse, error) {
	scope, err := s.teamAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid team id")
	}
	if err := s.store.DeleteTeam(ctx, scope.OrgID, id); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &identityv1.DeleteTeamResponse{Ok: true}, nil
}

// resolveUser accepts a username or a user id, as adding an organization member
// does: an interface carries names, and accepting only ids refused every request
// it made.
func (s *Server) resolveUser(ctx context.Context, ref string) (uuid.UUID, error) {
	if ref == "" {
		return uuid.Nil, status.Error(codes.InvalidArgument, "user is required")
	}
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}
	u, err := s.store.UserByUsername(ctx, ref)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.NotFound, "no user named %q", ref)
	}
	return u.ID, nil
}

// AddTeamMember puts a person in a team. They must already belong to the
// organization: a team is a narrower grant within it, not a way into it.
func (s *Server) AddTeamMember(ctx context.Context, req *identityv1.AddTeamMemberRequest) (*identityv1.AddTeamMemberResponse, error) {
	scope, err := s.teamAdmin(ctx)
	if err != nil {
		return nil, err
	}
	teamID, err := uuid.Parse(req.GetTeamId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid team_id")
	}
	userID, err := s.resolveUser(ctx, req.GetUser())
	if err != nil {
		return nil, err
	}
	member, err := s.store.IsOrgMember(ctx, scope.OrgID, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check membership: %v", err)
	}
	if !member {
		return nil, status.Error(codes.FailedPrecondition, "add them to the organization before adding them to a team")
	}
	if err := s.store.AddTeamMember(ctx, scope.OrgID, teamID, userID); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &identityv1.AddTeamMemberResponse{Ok: true}, nil
}

// RemoveTeamMember takes a person out of a team.
func (s *Server) RemoveTeamMember(ctx context.Context, req *identityv1.RemoveTeamMemberRequest) (*identityv1.RemoveTeamMemberResponse, error) {
	scope, err := s.teamAdmin(ctx)
	if err != nil {
		return nil, err
	}
	teamID, err := uuid.Parse(req.GetTeamId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid team_id")
	}
	userID, err := s.resolveUser(ctx, req.GetUser())
	if err != nil {
		return nil, err
	}
	if err := s.store.RemoveTeamMember(ctx, scope.OrgID, teamID, userID); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &identityv1.RemoveTeamMemberResponse{Ok: true}, nil
}

// ListTeamsForUser reports which teams a person belongs to.
//
// git-platform calls this to resolve a repository grant that names a team, rather
// than reading Identity's schema. A caller may ask about themselves; asking about
// someone else is an administrative read.
func (s *Server) ListTeamsForUser(ctx context.Context, req *identityv1.ListTeamsForUserRequest) (*identityv1.ListTeamsForUserResponse, error) {
	scope, err := s.teamMember(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}
	// A service asking on someone's behalf is how access is resolved at a push, so
	// a service credential may ask about anyone in its organization. A person may
	// only ask about themselves unless they administer the organization.
	if scope.ActorKind == "user" && scope.ActorID != userID {
		role, rerr := s.store.MemberRole(ctx, scope.OrgID, scope.ActorID)
		if rerr != nil || (role != "owner" && role != "admin") {
			return nil, status.Error(codes.PermissionDenied, "only an owner or admin may read another person's teams")
		}
	}
	teams, err := s.store.TeamsForUser(ctx, scope.OrgID, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "teams for user: %v", err)
	}
	out := make([]*identityv1.Team, 0, len(teams))
	for _, t := range teams {
		out = append(out, teamProto(t, nil))
	}
	return &identityv1.ListTeamsForUserResponse{Teams: out}, nil
}

// ResolveUsername answers one exact username with its user id.
//
// It exists so a repository grant can name someone who is not a member of the
// organization granting it: a name is what a person types. It is not a search — one
// exact match or NotFound — and only an organization's owner or admin, or an
// org-scoped service, may ask. Anything looser would let any authenticated account
// enumerate the deployment's users one guess at a time.
func (s *Server) ResolveUsername(ctx context.Context, req *identityv1.ResolveUsernameRequest) (*identityv1.ResolveUsernameResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	switch {
	case scope.ActorKind == "service":
		// An org-scoped service asking on an administrator's behalf, which is how
		// git-platform resolves a grant it was asked to make.
	case scope.ActorKind == "user":
		role, rerr := s.store.MemberRole(ctx, scope.OrgID, scope.ActorID)
		if rerr != nil || (role != "owner" && role != "admin") {
			return nil, status.Error(codes.PermissionDenied, "only an owner or admin may resolve a username")
		}
	default:
		return nil, status.Error(codes.PermissionDenied, "only a person or an organization service may resolve a username")
	}
	if req.GetUsername() == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	u, err := s.store.UserByUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "no user named %q", req.GetUsername())
	}
	return &identityv1.ResolveUsernameResponse{UserId: u.ID.String()}, nil
}
