package edge_test

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/edge"
)

type ciDownloadDouble struct {
	civ1.CIServiceClient
	chunks []*civ1.DownloadArtifactResponse
	err    error
	asked  string
}

func (c *ciDownloadDouble) DownloadArtifact(_ context.Context, in *civ1.DownloadArtifactRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[civ1.DownloadArtifactResponse], error) {
	c.asked = in.GetArtifactId()
	return &downloadStream{chunks: c.chunks, err: c.err}, nil
}

type downloadStream struct {
	grpc.ClientStream
	chunks []*civ1.DownloadArtifactResponse
	err    error
}

func (s *downloadStream) Recv() (*civ1.DownloadArtifactResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(s.chunks) == 0 {
		return nil, io.EOF
	}
	c := s.chunks[0]
	s.chunks = s.chunks[1:]
	return c, nil
}

// TestDownloadArtifactServesTheFile pins the route that did not exist: CI kept
// every artifact and offered no way to fetch one, so the artifact list was a
// list of names. The file is served as an attachment, never rendered inline —
// an artifact is whatever a repository's job wrote, and an HTML file served
// inline from the platform's own origin would run in a signed-in person's
// session.
func TestDownloadArtifactServesTheFile(t *testing.T) {
	g, _, _ := runDoubles()
	ci := &ciDownloadDouble{chunks: []*civ1.DownloadArtifactResponse{
		{Name: "report.txt", SizeBytes: 13, Data: []byte("coverage")},
		{Data: []byte(": 81%")},
	}}
	h := edge.Handlers(edge.Config{Git: g, CI: ci})

	rec := call(t, h, "downloadArtifact", http.MethodGet, "",
		map[string]string{"org": "acme", "repo": "platform", "id": "art-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if ci.asked != "art-1" {
		t.Fatalf("asked CI for artifact %q, want art-1", ci.asked)
	}
	if got := rec.Body.String(); got != "coverage: 81%" {
		t.Fatalf("body = %q", got)
	}
	disp, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil || disp != "attachment" || params["filename"] != "report.txt" {
		t.Fatalf("Content-Disposition = %q, want an attachment named report.txt", rec.Header().Get("Content-Disposition"))
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "13" {
		t.Fatalf("Content-Length = %q", got)
	}

	html := &ciDownloadDouble{chunks: []*civ1.DownloadArtifactResponse{
		{Name: "coverage.html", SizeBytes: 26, Data: []byte("<script>alert(1)</script>")},
	}}
	rec = call(t, edge.Handlers(edge.Config{Git: g, CI: html}), "downloadArtifact", http.MethodGet, "",
		map[string]string{"org": "acme", "repo": "platform", "id": "art-2"})
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("an HTML artifact was served as %q; it must never be renderable from the platform's origin", got)
	}
}

func (c *ciDownloadDouble) GetRun(_ context.Context, in *civ1.GetRunRequest, _ ...grpc.CallOption) (*civ1.GetRunResponse, error) {
	return &civ1.GetRunResponse{
		Run:  &civ1.WorkflowRunSummary{Id: in.GetId(), Status: "running", Ref: "refs/heads/main", CommitSha: "abc"},
		Jobs: []*civ1.WorkflowJobSummary{{Id: "job-1", Name: "build", Status: "success"}, {Id: "job-2", Name: "test", Status: "running"}},
	}, nil
}

// TestGetCIRunCarriesTheRun pins a shape mismatch the CI screen was built on:
// it read the run's status from body.run while the edge put the run's fields
// at the top level, so the screen never knew a run was running — its log was
// never refetched and the "live" marker never showed.
func TestGetCIRunCarriesTheRun(t *testing.T) {
	g, _, _ := runDoubles()
	h := edge.Handlers(edge.Config{Git: g, CI: &ciDownloadDouble{}})
	rec := call(t, h, "getCIRun", http.MethodGet, "", map[string]string{"org": "acme", "repo": "platform", "id": "run-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"run"`
		Jobs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Run.ID != "run-1" || body.Run.Status != "running" {
		t.Fatalf("run = %+v, want run-1 running under \"run\"", body.Run)
	}
	if len(body.Jobs) != 2 || body.Jobs[1].Status != "running" {
		t.Fatalf("jobs = %+v", body.Jobs)
	}
}

// TestDownloadArtifactReportsAbsence keeps a missing (or another
// organization's) artifact a 404, not a 200 with an empty file.
func TestDownloadArtifactReportsAbsence(t *testing.T) {
	g, _, _ := runDoubles()
	ci := &ciDownloadDouble{err: status.Error(codes.NotFound, "no such artifact")}
	rec := call(t, edge.Handlers(edge.Config{Git: g, CI: ci}), "downloadArtifact", http.MethodGet, "",
		map[string]string{"org": "acme", "repo": "platform", "id": "art-1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
