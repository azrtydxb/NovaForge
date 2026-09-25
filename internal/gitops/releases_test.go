package gitops_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
)

// newReleaseStore returns a release store backed by the real PostgreSQL and the
// real MinIO the suite is pointed at. Neither is doubled: the thing being proven
// here is that rows and objects agree, and an in-process object store would
// agree with anything.
func newReleaseStore(t *testing.T, root string) (*gitops.ReleaseStore, *blobstore.Client) {
	t.Helper()
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	url := gitopsDBURL(t)
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform schema: %v", err)
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// A bucket of its own per test run: release assets and CI artifacts share a
	// deployment's bucket, and a test that deleted by prefix would otherwise be
	// able to take another suite's objects with it.
	blobs, err := blobstore.New(ctx, blobstore.Options{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		Bucket:    "novaforge-test-rel-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("connect object storage: %v", err)
	}
	return gitops.NewReleaseStore(pool, root, blobs), blobs
}

// pushTag creates tag in the bare repository at repoPath, through a throwaway
// clone — the same way anything else in this package writes to a bare repo.
func pushTag(t *testing.T, repoPath, tag string) {
	t.Helper()
	work := t.TempDir()
	runGit(t, "", "clone", repoPath, work)
	runGit(t, work, "-c", "user.email=t@example.com", "-c", "user.name=T", "tag", tag)
	runGit(t, work, "push", "origin", tag)
}

// TestReleaseWithAssets is the whole point of a release: a tag a team can hand
// out, carrying files they can download. Tags and CI artifacts both existed and
// neither could do this — an artifact belongs to a job, not to a version.
//
// The three things this pins are the three that were wrong in every hand-rolled
// version of this: a release on a tag that does not exist (so the download link
// points at nothing), an asset that does not come back byte for byte, and a
// deleted release whose objects stay in the bucket forever.
func TestReleaseWithAssets(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	store, blobs := newReleaseStore(t, root)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	created, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "ledger"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	repoID := created.GetRepo().GetId()
	path := filepath.Join(root, orgID.String(), "ledger.git")
	seedInitialCommit(t, path)
	pushTag(t, path, "v1.0.0")

	t.Run("a release on a tag that does not exist is refused", func(t *testing.T) {
		_, err := store.CreateRelease(ctx, orgID, "ledger", "v9.9.9", "Ghost", "")
		if err == nil {
			t.Fatal("a release was created on a tag that does not exist")
		}
		if !strings.Contains(err.Error(), "v9.9.9") {
			t.Fatalf("the refusal does not name the missing tag: %v", err)
		}
	})

	rel, err := store.CreateRelease(ctx, orgID, "ledger", "v1.0.0", "First cut", "notes")
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if rel.Tag != "v1.0.0" || rel.Name != "First cut" || rel.Body != "notes" {
		t.Fatalf("release came back as %+v", rel)
	}
	if rel.RepoID.String() != repoID {
		t.Fatalf("release is on repository %s, want %s", rel.RepoID, repoID)
	}

	// A megabyte so the download cannot pass by accident on a body that fits in
	// one chunk: the streaming path has to reassemble it.
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("random payload: %v", err)
	}
	asset, err := store.AddAsset(ctx, orgID, rel.ID, "ledger-linux-arm64.tar.gz",
		"application/gzip", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("AddAsset: %v", err)
	}
	if asset.Size != int64(len(payload)) {
		t.Fatalf("asset size is %d, want %d", asset.Size, len(payload))
	}

	t.Run("the asset downloads byte for byte", func(t *testing.T) {
		got, rc, err := store.OpenAsset(ctx, orgID, "ledger", "v1.0.0", "ledger-linux-arm64.tar.gz")
		if err != nil {
			t.Fatalf("OpenAsset: %v", err)
		}
		defer rc.Close()
		if got.ContentType != "application/gzip" {
			t.Errorf("content type is %q, want application/gzip", got.ContentType)
		}
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read asset: %v", err)
		}
		if !bytes.Equal(body, payload) {
			t.Fatalf("the asset came back %d bytes and differs from the %d uploaded", len(body), len(payload))
		}
	})

	t.Run("the release lists its assets", func(t *testing.T) {
		list, err := store.ListReleases(ctx, orgID, "ledger")
		if err != nil {
			t.Fatalf("ListReleases: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("got %d releases, want 1", len(list))
		}
		if len(list[0].Assets) != 1 || list[0].Assets[0].Name != "ledger-linux-arm64.tar.gz" {
			t.Fatalf("release assets are %+v", list[0].Assets)
		}
		if list[0].Assets[0].Size != int64(len(payload)) {
			t.Fatalf("listed size is %d, want %d", list[0].Assets[0].Size, len(payload))
		}
	})

	t.Run("deleting the release deletes its blobstore objects", func(t *testing.T) {
		key := asset.BlobKey
		if _, err := blobs.Get(context.Background(), key); err != nil {
			t.Fatalf("the asset object is not in the bucket before the delete: %v", err)
		}
		if err := store.DeleteRelease(ctx, orgID, "ledger", "v1.0.0"); err != nil {
			t.Fatalf("DeleteRelease: %v", err)
		}
		// Rows going is the easy half. The object going is the half that was
		// always forgotten, and a bucket nobody can reach into is a bucket that
		// grows forever.
		if _, err := blobs.Get(context.Background(), key); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("the object %s survived the release's deletion: %v", key, err)
		}
		list, err := store.ListReleases(ctx, orgID, "ledger")
		if err != nil {
			t.Fatalf("ListReleases after delete: %v", err)
		}
		if len(list) != 0 {
			t.Fatalf("got %d releases after the delete, want 0", len(list))
		}
	})
}

// TestReleaseAssetKeysAreOrgScoped proves an asset cannot be reached from
// another organization by guessing a name. Two organizations here hold the same
// repository name, the same tag and the same asset name, which is the case a
// key built from those three names alone would collide on — one organization's
// upload would overwrite the other's release, and either could download it.
func TestReleaseAssetKeysAreOrgScoped(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	store, blobs := newReleaseStore(t, root)

	type side struct {
		org   uuid.UUID
		ctx   context.Context
		rel   gitops.Release
		asset gitops.Asset
		body  string
	}
	sides := make([]*side, 2)
	for i, body := range []string{"organization one's build", "organization two's build"} {
		s := &side{org: uuid.New(), body: body}
		s.ctx = scopedCtx(s.org)
		if _, err := srv.CreateRepo(s.ctx, &gitv1.CreateRepoRequest{Name: "ledger"}); err != nil {
			t.Fatalf("CreateRepo: %v", err)
		}
		path := filepath.Join(root, s.org.String(), "ledger.git")
		seedInitialCommit(t, path)
		pushTag(t, path, "v1.0.0")
		rel, err := store.CreateRelease(s.ctx, s.org, "ledger", "v1.0.0", "cut", "")
		if err != nil {
			t.Fatalf("CreateRelease: %v", err)
		}
		s.rel = rel
		asset, err := store.AddAsset(s.ctx, s.org, rel.ID, "build.bin", "application/octet-stream",
			strings.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("AddAsset: %v", err)
		}
		s.asset = asset
		sides[i] = s
	}
	a, b := sides[0], sides[1]

	want := "org/" + a.org.String() + "/repo/" + a.rel.RepoID.String() +
		"/release/" + a.rel.ID.String() + "/" + a.asset.ID.String()
	if a.asset.BlobKey != want {
		t.Fatalf("key is %q, want %q", a.asset.BlobKey, want)
	}
	if a.asset.BlobKey == b.asset.BlobKey {
		t.Fatal("two organizations' identically named assets share one object key")
	}

	// Each organization gets its own bytes back through its own repository, which
	// a shared key would have made impossible.
	for _, s := range sides {
		_, rc, err := store.OpenAsset(s.ctx, s.org, "ledger", "v1.0.0", "build.bin")
		if err != nil {
			t.Fatalf("OpenAsset: %v", err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read asset: %v", err)
		}
		if string(got) != s.body {
			t.Fatalf("organization %s downloaded %q, want %q", s.org, got, s.body)
		}
	}

	// And the org predicate is taken from the scope, so naming another
	// organization's repository, release or asset reads as absent.
	if _, err := store.ListReleases(b.ctx, b.org, a.rel.RepoID.String()); err == nil {
		t.Fatal("one organization listed another's releases by repository id")
	}
	if _, err := store.AddAsset(b.ctx, b.org, a.rel.ID, "sneak.bin", "application/octet-stream",
		strings.NewReader("x"), 1); err == nil {
		t.Fatal("one organization added an asset to another's release")
	}
	if err := store.DeleteRelease(b.ctx, b.org, a.rel.RepoID.String(), "v1.0.0"); err == nil {
		t.Fatal("one organization deleted another's release")
	}
	// The object itself is still there: the refusals above were refusals, not
	// deletions performed in the wrong organization.
	if _, err := blobs.Get(context.Background(), a.asset.BlobKey); err != nil {
		t.Fatalf("the first organization's object is gone after the second was refused: %v", err)
	}
}

// fakeDownloadStream collects what the server sends. It embeds grpc.ServerStream
// for the interface's sake only; nothing in the download path uses it.
type fakeDownloadStream struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*gitv1.DownloadReleaseAssetResponse
}

func (f *fakeDownloadStream) Context() context.Context { return f.ctx }
func (f *fakeDownloadStream) Send(m *gitv1.DownloadReleaseAssetResponse) error {
	// The server reuses one buffer between sends, so a collector that kept the
	// slice would end up with every message pointing at the last chunk read.
	cp := &gitv1.DownloadReleaseAssetResponse{
		Name: m.GetName(), SizeBytes: m.GetSizeBytes(), ContentType: m.GetContentType(),
		Data: append([]byte(nil), m.GetData()...),
	}
	f.msgs = append(f.msgs, cp)
	return nil
}

// fakeUploadStream feeds the server a sequence of frames.
type fakeUploadStream struct {
	grpc.ServerStream
	ctx    context.Context
	frames []*gitv1.UploadReleaseAssetRequest
	i      int
	reply  *gitv1.UploadReleaseAssetResponse
}

func (f *fakeUploadStream) Context() context.Context { return f.ctx }
func (f *fakeUploadStream) Recv() (*gitv1.UploadReleaseAssetRequest, error) {
	if f.i >= len(f.frames) {
		return nil, io.EOF
	}
	f.i++
	return f.frames[f.i-1], nil
}
func (f *fakeUploadStream) SendAndClose(m *gitv1.UploadReleaseAssetResponse) error {
	f.reply = m
	return nil
}

// TestReleaseAssetStreamsThroughTheRPC pins the seam the store tests leave open:
// the store can stream perfectly and the RPC in front of it can still read the
// whole object into one message, which is how a service dies serving a popular
// release. A megabyte asset must arrive in more than one frame, and the frames
// must reassemble byte for byte.
//
// The upload side is streamed for the same reason, and is the only way an asset
// reaches the platform, so it is exercised here rather than through the store.
func TestReleaseAssetStreamsThroughTheRPC(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	store, _ := newReleaseStore(t, root)
	srv.Releases = store
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "ledger"}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	path := filepath.Join(root, orgID.String(), "ledger.git")
	seedInitialCommit(t, path)
	pushTag(t, path, "v2.0.0")

	if _, err := srv.CreateRelease(ctx, &gitv1.CreateReleaseRequest{
		Repo: "ledger", Tag: "v2.0.0", Name: "Second cut", Body: "notes",
	}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("random payload: %v", err)
	}
	// Frames of 64 KiB: an upload arrives in pieces, and the server must not
	// depend on the first frame carrying the whole body.
	frames := []*gitv1.UploadReleaseAssetRequest{}
	for off := 0; off < len(payload); off += 64 << 10 {
		end := min(off+64<<10, len(payload))
		f := &gitv1.UploadReleaseAssetRequest{Data: payload[off:end]}
		if off == 0 {
			f.Repo, f.Tag, f.Name, f.ContentType = "ledger", "v2.0.0", "ledger.tar.gz", "application/gzip"
		}
		frames = append(frames, f)
	}
	up := &fakeUploadStream{ctx: ctx, frames: frames}
	if err := srv.UploadReleaseAsset(up); err != nil {
		t.Fatalf("UploadReleaseAsset: %v", err)
	}
	if up.reply.GetAsset().GetSizeBytes() != int64(len(payload)) {
		t.Fatalf("the upload recorded %d bytes, want %d", up.reply.GetAsset().GetSizeBytes(), len(payload))
	}

	down := &fakeDownloadStream{ctx: ctx}
	if err := srv.DownloadReleaseAsset(&gitv1.DownloadReleaseAssetRequest{
		Repo: "ledger", Tag: "v2.0.0", Name: "ledger.tar.gz",
	}, down); err != nil {
		t.Fatalf("DownloadReleaseAsset: %v", err)
	}
	if len(down.msgs) < 2 {
		t.Fatalf("a %d-byte asset was sent in %d message(s): it is being buffered, not streamed",
			len(payload), len(down.msgs))
	}
	if down.msgs[0].GetName() != "ledger.tar.gz" || down.msgs[0].GetSizeBytes() != int64(len(payload)) {
		t.Errorf("the first message does not describe the asset: %+v", down.msgs[0])
	}
	var got []byte
	for _, m := range down.msgs {
		got = append(got, m.GetData()...)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the reassembled asset is %d bytes and differs from the %d uploaded", len(got), len(payload))
	}

	// And another organization asking for the same asset is told it is absent,
	// not refused: the refusal itself would confirm the asset exists.
	other := &fakeDownloadStream{ctx: scopedCtx(uuid.New())}
	err := srv.DownloadReleaseAsset(&gitv1.DownloadReleaseAssetRequest{
		Repo: "ledger", Tag: "v2.0.0", Name: "ledger.tar.gz",
	}, other)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("another organization's download answered %v, want NotFound", err)
	}
}
