package work

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// ScanResult is what one on-demand maintenance scan produced.
type ScanResult struct {
	Findings      int
	ProposedKeys  []string
	ScannerErrors []string
}

// Scanner runs the maintenance scanners against one repository and proposes
// Work Items for what they find. It lives outside this package (the scanners
// need the git service and the analysis tools), and is wired in by the
// service binary.
type Scanner func(ctx context.Context, orgID, repoID uuid.UUID) (ScanResult, error)

// SetScanner wires on-demand maintenance scans. Left nil, ScanRepository
// reports that this deployment cannot scan rather than returning an empty,
// clean-looking result.
func (s *GRPCServer) SetScanner(sc Scanner) { s.scanner = sc }

// ScanRepository runs the maintenance scanners against a repository now.
//
// The sweep runs on an interval measured from process start, and every deploy
// restarts it: on a cluster deployed more often than the interval, no sweep
// ever ran and nothing was ever proposed. A person can now ask for one.
func (s *GRPCServer) ScanRepository(ctx context.Context, req *workv1.ScanRepositoryRequest) (*workv1.ScanRepositoryResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	if scope.ActorKind != "user" {
		return nil, status.Error(codes.PermissionDenied, "a scan on demand is requested by a person")
	}
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if s.scanner == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment has no maintenance scanners wired")
	}
	res, err := s.scanner(ctx, scope.OrgID, repoID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "scan: %v", err)
	}
	return &workv1.ScanRepositoryResponse{
		Findings:             int32(res.Findings),
		ProposedWorkItemKeys: res.ProposedKeys,
		ScannerErrors:        res.ScannerErrors,
	}, nil
}
