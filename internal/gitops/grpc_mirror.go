package gitops

// The import and mirroring RPCs. They live in their own file rather than in
// grpc.go because the credential handling is the whole point of them and is
// worth reading in one place.

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// ImportRepo clones an existing repository from another Git host into the
// caller's organization.
//
// Nothing about the request is logged. The credential arrives in it, and a log
// line is a read like any other — the same reason the webhook handlers log no
// request body.
func (s *Server) ImportRepo(ctx context.Context, req *gitv1.ImportRepoRequest) (*gitv1.ImportRepoResponse, error) {
	if s.Mirrors == nil {
		return nil, status.Error(codes.FailedPrecondition, "this deployment cannot import repositories")
	}
	if _, err := scopeFromContext(ctx); err != nil {
		return nil, err
	}
	imported, err := s.Mirrors.ImportRepo(ctx, ImportRequest{
		Name:       req.GetName(),
		Remote:     req.GetRemote(),
		Credential: req.GetCredential(),
		Mirror:     req.GetMirror(),
		Interval:   mirrorInterval(req.GetIntervalSeconds()),
	})
	if err != nil {
		return nil, mirrorStatus(err)
	}
	return &gitv1.ImportRepoResponse{
		Repo: &gitv1.Repo{
			Id:            imported.ID.String(),
			OrgId:         imported.OrgID.String(),
			Name:          imported.Name,
			DefaultBranch: imported.DefaultBranch,
		},
		Mirror: toProtoMirror(imported.Mirror),
	}, nil
}

// GetMirror reports what a repository follows, if anything.
func (s *Server) GetMirror(ctx context.Context, req *gitv1.GetMirrorRequest) (*gitv1.GetMirrorResponse, error) {
	if s.Mirrors == nil {
		return nil, status.Error(codes.FailedPrecondition, "this deployment has no mirroring")
	}
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}
	mirror, err := s.Mirrors.GetMirror(ctx, row.ID)
	if err != nil {
		return nil, mirrorStatus(err)
	}
	return &gitv1.GetMirrorResponse{Mirror: toProtoMirror(&mirror)}, nil
}

// SetMirror points an existing repository at an upstream, or repoints one.
func (s *Server) SetMirror(ctx context.Context, req *gitv1.SetMirrorRequest) (*gitv1.SetMirrorResponse, error) {
	if s.Mirrors == nil {
		return nil, status.Error(codes.FailedPrecondition, "this deployment has no mirroring")
	}
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}
	if err := s.Mirrors.SetMirror(ctx, row.ID, req.GetRemote(), req.GetCredential(),
		mirrorInterval(req.GetIntervalSeconds())); err != nil {
		return nil, mirrorStatus(err)
	}
	mirror, err := s.Mirrors.GetMirror(ctx, row.ID)
	if err != nil {
		return nil, mirrorStatus(err)
	}
	return &gitv1.SetMirrorResponse{Mirror: toProtoMirror(&mirror)}, nil
}

// DeleteMirror stops a repository following upstream, which also makes it
// writable again.
func (s *Server) DeleteMirror(ctx context.Context, req *gitv1.DeleteMirrorRequest) (*gitv1.DeleteMirrorResponse, error) {
	if s.Mirrors == nil {
		return nil, status.Error(codes.FailedPrecondition, "this deployment has no mirroring")
	}
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}
	if err := s.Mirrors.DeleteMirror(ctx, row.ID); err != nil {
		return nil, mirrorStatus(err)
	}
	return &gitv1.DeleteMirrorResponse{}, nil
}

// mirrorInterval maps the wire's interval_seconds onto a duration.
//
// 0 on the wire is indistinguishable from a field nobody set, so it means the
// platform's default rather than "refresh on every pass" — which is what the
// store means by a zero duration, and what an operator who said nothing would
// least want. A caller who really wants every pass is a test, and it calls the
// store directly.
func mirrorInterval(seconds int32) time.Duration {
	if seconds <= 0 {
		return DefaultMirrorInterval
	}
	return time.Duration(seconds) * time.Second
}

func toProtoMirror(m *Mirror) *gitv1.Mirror {
	if m == nil {
		return nil
	}
	out := &gitv1.Mirror{
		RepoId:          m.RepoID.String(),
		Remote:          m.Remote,
		IntervalSeconds: int32(m.Interval.Seconds()),
		LastError:       m.LastError,
		HasCredential:   m.HasCredential,
	}
	if m.LastSyncedAt != nil {
		out.LastSyncedAt = m.LastSyncedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// mirrorStatus maps this package's mirror errors onto gRPC codes. Without it
// every one of them would surface as Unknown, and the edge would answer 500 for
// a repository that simply is not a mirror.
func mirrorStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNoMirror):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrNoKEK):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrMirror):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		// An import failure is usually the remote: unreachable, private, or not a
		// repository. That is the caller's input, not the platform's fault, so it
		// must not read as an internal error — somebody would go looking at the
		// service instead of at the URL they typed.
		return status.Error(codes.InvalidArgument, err.Error())
	}
}
