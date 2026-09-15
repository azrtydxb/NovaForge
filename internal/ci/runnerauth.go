package ci

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/novaforge/novaforge/internal/authz"
)

// authenticateRunner checks token against the hash Register stored for
// runnerID, in constant time. The token was generated and returned by
// Register and was never checked by anything: ConnectRequest did not even
// carry it, so a runner id — which appears in job records — was a runner's
// whole identity.
func (s *Server) authenticateRunner(ctx context.Context, runnerID uuid.UUID, token string) error {
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) == 0 {
		return status.Error(codes.PermissionDenied, "a runner must present the token it was registered with")
	}
	_, want, err := s.store.RunnerTokenHash(ctx, runnerID)
	if err != nil {
		return status.Error(codes.PermissionDenied, "unknown runner or wrong token")
	}
	got := sha256.Sum256(raw)
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return status.Error(codes.PermissionDenied, "unknown runner or wrong token")
	}
	return nil
}

// jobIsRunners refuses unless jobID was dispatched to runnerID.
func (s *Server) jobIsRunners(ctx context.Context, jobID, runnerID uuid.UUID) error {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return status.Error(codes.NotFound, "no such job")
	}
	if job.RunnerID == nil || *job.RunnerID != runnerID {
		return status.Error(codes.PermissionDenied, "this job is not assigned to that runner")
	}
	return nil
}

// registrationScope is the caller's scope, if it may enrol a runner.
func registrationScope(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return authz.Scope{}, status.Error(codes.PermissionDenied, "registering a runner needs a credential for its organization")
	}
	switch {
	case scope.ActorKind == "user" && (scope.Role == "owner" || scope.Role == "admin"):
	case scope.ActorKind == "service":
	default:
		return authz.Scope{}, status.Errorf(codes.PermissionDenied,
			"only an organization owner or admin, or the platform, may register a runner; not a %s with role %q", scope.ActorKind, scope.Role)
	}
	return scope, nil
}
