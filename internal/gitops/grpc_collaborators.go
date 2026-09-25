package gitops

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
)

// UserResolver turns a username into a user id. Users live in Identity's schema,
// which git-platform may not read, so a grant naming a person by name is resolved
// through its RPC — the same reason a grant naming a team is.
type UserResolver func(ctx context.Context, name string) (uuid.UUID, error)

// NewIdentityUserResolver resolves a username through Identity.
func NewIdentityUserResolver(client identityv1.IdentityServiceClient) UserResolver {
	return func(ctx context.Context, name string) (uuid.UUID, error) {
		resp, err := client.ResolveUsername(ctx, &identityv1.ResolveUsernameRequest{Username: name})
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(resp.GetUserId())
	}
}

// collaboratorAdmin checks the caller may change who reaches this repository.
//
// Granting access is an organization-boundary decision, so it takes the same owners
// and admins that membership does — and it deliberately refuses a repository-limited
// scope: a collaborator who could grant must not be able to widen their own reach or
// hand the repository to someone else.
func (s *Server) collaboratorAdmin(ctx context.Context, repoRef string) (repoRow, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return repoRow{}, err
	}
	if scope.RepoLimited {
		return repoRow{}, status.Error(codes.PermissionDenied,
			"this credential reaches one repository and may not change who else does")
	}
	if !scope.IsOrgAdmin() {
		return repoRow{}, status.Error(codes.PermissionDenied, "only an owner or admin may grant repository access")
	}
	return s.repoByName(ctx, scope.OrgID, repoRef)
}

func (s *Server) collaboratorSubject(ctx context.Context, user, team string) (uuid.UUID, uuid.UUID, error) {
	var userID, teamID uuid.UUID
	if team != "" {
		id, err := uuid.Parse(team)
		if err != nil {
			return uuid.Nil, uuid.Nil, status.Error(codes.InvalidArgument, "invalid team_id")
		}
		teamID = id
	}
	if user != "" {
		if id, err := uuid.Parse(user); err == nil {
			userID = id
		} else {
			if s.Users == nil {
				return uuid.Nil, uuid.Nil, status.Error(codes.Unimplemented,
					"this deployment cannot resolve a username; grant by user id")
			}
			resolved, rerr := s.Users(ctx, user)
			if rerr != nil {
				return uuid.Nil, uuid.Nil, status.Errorf(codes.NotFound, "no user named %q", user)
			}
			userID = resolved
		}
	}
	return userID, teamID, nil
}

// AddCollaborator grants one repository to a person or a team.
func (s *Server) AddCollaborator(ctx context.Context, req *gitv1.AddCollaboratorRequest) (*gitv1.AddCollaboratorResponse, error) {
	if s.Collaborators == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment grants no repository access")
	}
	repo, err := s.collaboratorAdmin(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	g := req.GetGrant()
	userID, teamID, err := s.collaboratorSubject(ctx, g.GetUser(), g.GetTeamId())
	if err != nil {
		return nil, err
	}
	if err := s.Collaborators.AddCollaborator(ctx, repo.OrgID, repo.ID, userID, teamID, g.GetRole()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &gitv1.AddCollaboratorResponse{Ok: true}, nil
}

// RemoveCollaborator revokes a grant.
func (s *Server) RemoveCollaborator(ctx context.Context, req *gitv1.RemoveCollaboratorRequest) (*gitv1.RemoveCollaboratorResponse, error) {
	if s.Collaborators == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment grants no repository access")
	}
	repo, err := s.collaboratorAdmin(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	userID, teamID, err := s.collaboratorSubject(ctx, req.GetUser(), req.GetTeamId())
	if err != nil {
		return nil, err
	}
	if err := s.Collaborators.RemoveCollaborator(ctx, repo.OrgID, repo.ID, userID, teamID); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &gitv1.RemoveCollaboratorResponse{Ok: true}, nil
}

// ListCollaborators returns a repository's grants. A member may read them; a
// repository-limited credential may not, because who else holds a repository is the
// organization's business and not the guest's.
func (s *Server) ListCollaborators(ctx context.Context, req *gitv1.ListCollaboratorsRequest) (*gitv1.ListCollaboratorsResponse, error) {
	if s.Collaborators == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment grants no repository access")
	}
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if scope.RepoLimited {
		return nil, status.Error(codes.PermissionDenied,
			"this credential reaches one repository and may not read who else does")
	}
	repo, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}
	grants, err := s.Collaborators.ListCollaborators(ctx, repo.OrgID, repo.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list collaborators: %v", err)
	}
	out := make([]*gitv1.Collaborator, 0, len(grants))
	for _, g := range grants {
		p := &gitv1.Collaborator{Role: g.Role}
		if g.UserID != uuid.Nil {
			p.User = g.UserID.String()
		}
		if g.TeamID != uuid.Nil {
			p.TeamId = g.TeamID.String()
		}
		out = append(out, p)
	}
	return &gitv1.ListCollaboratorsResponse{Grants: out}, nil
}
