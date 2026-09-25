package gitops

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// ForkRepo forks a repository the caller can see into a new repository of their
// own. The parent is resolved from the caller's organization scope, never from
// the request alone, so naming another organization's repository finds nothing
// instead of copying it.
func (s *Server) ForkRepo(ctx context.Context, req *gitv1.ForkRepoRequest) (*gitv1.ForkRepoResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRepo() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo is required")
	}
	// The request may name the parent by id or by name, exactly as every other
	// repository RPC accepts both; forkRepo needs the id, so it is resolved here.
	parent, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}
	toOrg := uuid.Nil
	if raw := req.GetToOrg(); raw != "" {
		toOrg, err = uuid.Parse(raw)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "to_org must be an organization id")
		}
	}
	fork, err := s.forkRepo(ctx, parent.ID, toOrg, req.GetName())
	if err != nil {
		return nil, err
	}
	return &gitv1.ForkRepoResponse{Repo: toProtoRepo(fork)}, nil
}
