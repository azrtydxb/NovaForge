package gitops

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// releaseChunk is the size of each message an asset download sends, well under
// gRPC's default 4 MiB message limit. It matches CI's artifact download for the
// same reason: it is the largest frame that cannot trip the limit after protobuf
// framing, whatever the metadata on the first message.
const releaseChunk = 256 << 10

// releases returns the store or a refusal naming the reason. A deployment with no
// object storage configured is a configuration fact, and answering Unimplemented
// would send an operator looking for a version of the platform that has releases.
func (s *Server) releases() (*ReleaseStore, error) {
	if s.Releases == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this deployment has no object storage configured, so releases are unavailable")
	}
	return s.Releases, nil
}

// releaseErr maps a store error onto a status. Absence is NotFound whether the
// release does not exist or belongs to another organization: the two must be
// indistinguishable, or the error itself tells a caller what another organization
// holds.
func releaseErr(err error) error {
	switch {
	case errors.Is(err, ErrNoSuchTag):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrReleaseNotFound), errors.Is(err, ErrAssetNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrReleaseExists):
		return status.Error(codes.AlreadyExists, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// releaseWriteAllowed resolves the repository and refuses a write to it.
//
// An archived repository accepts no writes, and publishing a release is a write:
// a repository frozen for the record should not gain new published downloads.
// An agent is held to the same capability grant the transports hold it to for the
// tag itself — publishing refs/tags/v1 as a release and pushing refs/tags/v1 are
// the same authority, and the API and the transport disagreeing about that is a
// defect this codebase has already had once.
func (s *Server) releaseWriteAllowed(ctx context.Context, repoRef, tag string) error {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return err
	}
	repo, err := s.repoByName(ctx, scope.OrgID, repoRef)
	if err != nil {
		return err
	}
	if repo.Archived {
		return status.Error(codes.FailedPrecondition, ErrArchived.Error())
	}
	if tag != "" {
		if err := s.authorizeAgentWrite(ctx, scope, "refs/tags/"+tag); err != nil {
			return err
		}
	}
	return nil
}

// CreateRelease publishes an existing tag as a release.
func (s *Server) CreateRelease(ctx context.Context, req *gitv1.CreateReleaseRequest) (*gitv1.CreateReleaseResponse, error) {
	store, err := s.releases()
	if err != nil {
		return nil, err
	}
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.releaseWriteAllowed(ctx, req.GetRepo(), req.GetTag()); err != nil {
		return nil, err
	}
	rel, err := store.CreateRelease(ctx, scope.OrgID, req.GetRepo(), req.GetTag(), req.GetName(), req.GetBody())
	if err != nil {
		return nil, releaseErr(err)
	}
	return &gitv1.CreateReleaseResponse{Release: releaseProto(rel)}, nil
}

// ListReleases returns a repository's releases, newest first.
func (s *Server) ListReleases(ctx context.Context, req *gitv1.ListReleasesRequest) (*gitv1.ListReleasesResponse, error) {
	store, err := s.releases()
	if err != nil {
		return nil, err
	}
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	list, err := store.ListReleases(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, releaseErr(err)
	}
	out := make([]*gitv1.Release, 0, len(list))
	for _, r := range list {
		out = append(out, releaseProto(r))
	}
	return &gitv1.ListReleasesResponse{Releases: out}, nil
}

// DeleteRelease removes a release and the objects its assets hold.
func (s *Server) DeleteRelease(ctx context.Context, req *gitv1.DeleteReleaseRequest) (*gitv1.DeleteReleaseResponse, error) {
	store, err := s.releases()
	if err != nil {
		return nil, err
	}
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.releaseWriteAllowed(ctx, req.GetRepo(), req.GetTag()); err != nil {
		return nil, err
	}
	if err := store.DeleteRelease(ctx, scope.OrgID, req.GetRepo(), req.GetTag()); err != nil {
		return nil, releaseErr(err)
	}
	return &gitv1.DeleteReleaseResponse{}, nil
}

// UploadReleaseAsset receives an asset as a stream of frames and stores it.
//
// The frames become an io.Reader the store passes straight to object storage, so
// nothing larger than one frame is ever held here. Buffering the upload to learn
// its length first — the obvious way to satisfy an object store that wants a
// size — would put the whole asset in this process's memory.
func (s *Server) UploadReleaseAsset(stream gitv1.GitService_UploadReleaseAssetServer) error {
	store, err := s.releases()
	if err != nil {
		return err
	}
	ctx := stream.Context()
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		return status.Error(codes.InvalidArgument, "an upload needs at least one frame naming the release and the asset")
	}
	if err != nil {
		return err
	}
	if err := s.releaseWriteAllowed(ctx, first.GetRepo(), first.GetTag()); err != nil {
		return err
	}
	rel, err := store.GetRelease(ctx, scope.OrgID, first.GetRepo(), first.GetTag())
	if err != nil {
		return releaseErr(err)
	}

	body := &streamReader{first: first.GetData(), recv: func() ([]byte, error) {
		msg, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return msg.GetData(), nil
	}}
	// A negative size means "unknown length", which the multipart uploader
	// handles; the store records what actually arrived rather than a claim.
	asset, err := store.AddAsset(ctx, scope.OrgID, rel.ID, first.GetName(), first.GetContentType(), body, -1)
	if err != nil {
		return releaseErr(err)
	}
	return stream.SendAndClose(&gitv1.UploadReleaseAssetResponse{Asset: assetProto(asset)})
}

// streamReader turns a sequence of received frames into an io.Reader. It holds
// one frame at a time, which is the whole point.
type streamReader struct {
	first []byte
	buf   []byte
	recv  func() ([]byte, error)
	done  bool
}

func (r *streamReader) Read(p []byte) (int, error) {
	if r.first != nil {
		r.buf, r.first = r.first, nil
	}
	for len(r.buf) == 0 {
		if r.done {
			return 0, io.EOF
		}
		next, err := r.recv()
		if errors.Is(err, io.EOF) {
			r.done = true
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		r.buf = next
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// DownloadReleaseAsset streams one asset to a member of the organization that
// holds it. An asset of another organization reads as absent.
func (s *Server) DownloadReleaseAsset(req *gitv1.DownloadReleaseAssetRequest, stream gitv1.GitService_DownloadReleaseAssetServer) error {
	store, err := s.releases()
	if err != nil {
		return err
	}
	ctx := stream.Context()
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return err
	}
	asset, rc, err := store.OpenAsset(ctx, scope.OrgID, req.GetRepo(), req.GetTag(), req.GetName())
	if err != nil {
		return releaseErr(err)
	}
	defer rc.Close()

	buf := make([]byte, releaseChunk)
	first := true
	for {
		n, rerr := io.ReadFull(rc, buf)
		// An empty asset is a legitimate file, so the first message is sent even
		// when there is nothing to put in it: a client that received no message
		// at all cannot tell an empty file from a missing one.
		if n > 0 || first {
			msg := &gitv1.DownloadReleaseAssetResponse{Data: buf[:n]}
			if first {
				msg.Name, msg.SizeBytes, msg.ContentType = asset.Name, asset.Size, asset.ContentType
				first = false
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			return nil
		}
		if rerr != nil {
			return status.Errorf(codes.Internal, "read release asset: %v", rerr)
		}
	}
}

func releaseProto(r Release) *gitv1.Release {
	assets := make([]*gitv1.ReleaseAsset, 0, len(r.Assets))
	for _, a := range r.Assets {
		assets = append(assets, assetProto(a))
	}
	return &gitv1.Release{
		Id: r.ID.String(), RepoId: r.RepoID.String(),
		Tag: r.Tag, Name: r.Name, Body: r.Body,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		Assets:    assets,
	}
}

func assetProto(a Asset) *gitv1.ReleaseAsset {
	return &gitv1.ReleaseAsset{
		Id: a.ID.String(), Name: a.Name,
		SizeBytes: a.Size, ContentType: a.ContentType,
	}
}
