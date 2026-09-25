package edge_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/edge"
)

// releaseDouble stands in for git-platform's streaming release RPCs. The bytes it
// receives and the frames it was given are both recorded, because what is being
// proven here is the edge's framing, not the service behind it.
type releaseDouble struct {
	gitv1.GitServiceClient
	download  []*gitv1.DownloadReleaseAssetResponse
	uploaded  *uploadRecorder
	askedTag  string
	askedName string
}

func (d *releaseDouble) DownloadReleaseAsset(_ context.Context, in *gitv1.DownloadReleaseAssetRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[gitv1.DownloadReleaseAssetResponse], error) {
	d.askedTag, d.askedName = in.GetTag(), in.GetName()
	return &releaseDownloadStream{frames: d.download}, nil
}

func (d *releaseDouble) UploadReleaseAsset(_ context.Context, _ ...grpc.CallOption) (grpc.ClientStreamingClient[gitv1.UploadReleaseAssetRequest, gitv1.UploadReleaseAssetResponse], error) {
	d.uploaded = &uploadRecorder{}
	return d.uploaded, nil
}

type releaseDownloadStream struct {
	grpc.ClientStream
	frames []*gitv1.DownloadReleaseAssetResponse
}

func (s *releaseDownloadStream) Recv() (*gitv1.DownloadReleaseAssetResponse, error) {
	if len(s.frames) == 0 {
		return nil, io.EOF
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return f, nil
}

type uploadRecorder struct {
	grpc.ClientStream
	frames int
	body   bytes.Buffer
	meta   *gitv1.UploadReleaseAssetRequest
	widest int
}

func (u *uploadRecorder) Send(m *gitv1.UploadReleaseAssetRequest) error {
	if u.frames == 0 {
		u.meta = m
	}
	u.frames++
	if len(m.GetData()) > u.widest {
		u.widest = len(m.GetData())
	}
	u.body.Write(m.GetData())
	return nil
}

func (u *uploadRecorder) CloseAndRecv() (*gitv1.UploadReleaseAssetResponse, error) {
	return &gitv1.UploadReleaseAssetResponse{Asset: &gitv1.ReleaseAsset{
		Id: "asset-1", Name: u.meta.GetName(), SizeBytes: int64(u.body.Len()),
		ContentType: u.meta.GetContentType(),
	}}, nil
}

// TestUploadReleaseAssetFramesTheBody pins the edge's half of a streamed upload.
// The body is read a frame at a time, so a release asset — a build output, often
// hundreds of megabytes — never sits in the edge's memory; and the release and the
// asset are named on the first frame only, because the server reads them there and
// repeating them on every frame would put the whole of the metadata cost on the
// wire per chunk.
func TestUploadReleaseAssetFramesTheBody(t *testing.T) {
	g := &releaseDouble{}
	h := edge.Handlers(edge.Config{Git: g})

	// Comfortably more than one frame's worth, so a handler that sent the body in
	// one message is visible as a single frame.
	body := strings.Repeat("x", (256<<10)+1234)
	req := httptest.NewRequest(http.MethodPost, "/?name=build.tar.gz", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/gzip")
	rc := chi.NewRouteContext()
	rc.URLParams.Add("repo", "ledger")
	rc.URLParams.Add("tag", "v1.0.0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	h["uploadReleaseAsset"](rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("upload answered %d: %s", rec.Code, rec.Body.String())
	}
	if g.uploaded.body.String() != body {
		t.Fatalf("the service received %d bytes, want %d", g.uploaded.body.Len(), len(body))
	}
	if g.uploaded.frames < 2 {
		t.Fatalf("the %d-byte body was sent in %d frame(s): it is being buffered, not streamed",
			len(body), g.uploaded.frames)
	}
	if g.uploaded.widest > 256<<10 {
		t.Errorf("a frame carried %d bytes, past the 256 KiB frame size", g.uploaded.widest)
	}
	if g.uploaded.meta.GetRepo() != "ledger" || g.uploaded.meta.GetTag() != "v1.0.0" ||
		g.uploaded.meta.GetName() != "build.tar.gz" || g.uploaded.meta.GetContentType() != "application/gzip" {
		t.Errorf("the first frame does not name the release and the asset: %+v", g.uploaded.meta)
	}
}

// TestUploadReleaseAssetNeedsAName refuses an upload with nothing to call the
// file: an asset is downloaded by its name, and one stored without a name would
// be unreachable rather than merely untidy.
func TestUploadReleaseAssetNeedsAName(t *testing.T) {
	h := edge.Handlers(edge.Config{Git: &releaseDouble{}})
	rec := call(t, h, "uploadReleaseAsset", http.MethodPost, "bytes",
		map[string]string{"repo": "ledger", "tag": "v1.0.0"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unnamed upload answered %d, want 400", rec.Code)
	}
}

// TestDownloadReleaseAssetServesTheFileDefensively pins how an uploaded file is
// served back. It is whatever somebody uploaded, so it is an attachment, its type
// is derived from the name rather than echoed from the upload, and it may not be
// sniffed or run on the platform's origin — the uploader's own content type is
// attacker-chosen text, and honouring text/html there is stored cross-site
// scripting.
func TestDownloadReleaseAssetServesTheFileDefensively(t *testing.T) {
	g := &releaseDouble{download: []*gitv1.DownloadReleaseAssetResponse{
		{Name: "notes.html", SizeBytes: 12, ContentType: "text/html", Data: []byte("hello ")},
		{Data: []byte("world!")},
	}}
	h := edge.Handlers(edge.Config{Git: g})
	rec := call(t, h, "downloadReleaseAsset", http.MethodGet, "",
		map[string]string{"repo": "ledger", "tag": "v1.0.0", "name": "notes.html"})

	if rec.Code != http.StatusOK {
		t.Fatalf("download answered %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello world!" {
		t.Fatalf("the body is %q, want the frames reassembled", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type is %q: an uploaded file's own type was echoed back", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition is %q, want an attachment", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the response may be sniffed into something renderable")
	}
	if rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Error("the response is not sandboxed")
	}
	if rec.Header().Get("Content-Length") != "12" {
		t.Errorf("Content-Length is %q, want 12 so a truncated body is detectable",
			rec.Header().Get("Content-Length"))
	}
}

// TestDownloadReleaseAssetReportsAbsence pins that nothing is written before the
// first frame arrives. A handler that had already sent 200 would serve an absent
// asset as an empty file, which every client would save as a corrupt download.
func TestDownloadReleaseAssetReportsAbsence(t *testing.T) {
	h := edge.Handlers(edge.Config{Git: &releaseDouble{}})
	rec := call(t, h, "downloadReleaseAsset", http.MethodGet, "",
		map[string]string{"repo": "ledger", "tag": "v1.0.0", "name": "gone.bin"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an absent asset answered %d, want 404", rec.Code)
	}
}

// TestReleaseTagWithSlashesReachesTheService pins the same defect the repository
// browser had for agent branches: a tag may contain slashes ("v1.0.0/rc1" is an
// ordinary release candidate), a client must percent-encode them to keep the tag
// in one path segment, and chi hands the segment back still encoded. Undecoded,
// the service is asked for a tag named "v1.0.0%2Frc1", which exists nowhere, and
// the release is addressable by nothing.
func TestReleaseTagWithSlashesReachesTheService(t *testing.T) {
	g := &releaseDouble{download: []*gitv1.DownloadReleaseAssetResponse{
		{Name: "build v1.bin", SizeBytes: 2, Data: []byte("ok")},
	}}
	h := edge.Handlers(edge.Config{Git: g})
	rec := call(t, h, "downloadReleaseAsset", http.MethodGet, "",
		map[string]string{"repo": "ledger", "tag": "v1.0.0%2Frc1", "name": "build%20v1.bin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("download answered %d: %s", rec.Code, rec.Body.String())
	}
	if g.askedTag != "v1.0.0/rc1" {
		t.Errorf("the service was asked for tag %q, want v1.0.0/rc1", g.askedTag)
	}
	if g.askedName != "build v1.bin" {
		t.Errorf("the service was asked for asset %q, want \"build v1.bin\"", g.askedName)
	}
}

// TestReleaseJSONCarriesWhatTheScreenReads pins the field names the Repos screen
// reads. A renamed field is a panel that shows a release with no assets, which is
// indistinguishable from a release that has none.
func TestReleaseJSONCarriesWhatTheScreenReads(t *testing.T) {
	body := edge.ReleaseJSON(&gitv1.Release{
		Id: "rel-1", RepoId: "repo-1", Tag: "v1.0.0", Name: "First cut", Body: "notes",
		CreatedAt: "2026-09-25T10:00:00Z",
		Assets: []*gitv1.ReleaseAsset{
			{Id: "a-1", Name: "build.bin", SizeBytes: 42, ContentType: "application/octet-stream"},
		},
	})
	for _, k := range []string{"id", "repo_id", "tag", "name", "body", "created_at", "assets"} {
		if _, ok := body[k]; !ok {
			t.Errorf("releaseJSON omits %q", k)
		}
	}
	assets, ok := body["assets"].([]map[string]any)
	if !ok || len(assets) != 1 {
		t.Fatalf("assets = %#v, want one asset", body["assets"])
	}
	for _, k := range []string{"id", "name", "size_bytes", "content_type"} {
		if _, ok := assets[0][k]; !ok {
			t.Errorf("the asset omits %q", k)
		}
	}
}
