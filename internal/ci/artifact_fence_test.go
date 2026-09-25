package ci_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/ci"
)

type delayedArtifact struct {
	entered chan struct{}
	release chan struct{}
	source  io.Reader
	first   bool
}

func (r *delayedArtifact) Read(p []byte) (int, error) {
	if !r.first {
		r.first = true
		close(r.entered)
		<-r.release
	}
	return r.source.Read(p)
}

func TestArtifactReplacementDuringUploadCannotPublish(t *testing.T) {
	s := newCredentialStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	job := s.job("refs/heads/main", "upload", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	if err := s.store.StartClaimedJob(ctx, job, s.runnerID); err != nil {
		t.Fatal(err)
	}
	artifacts := ci.NewArtifactStore(s.pool, artifactsBlobstore(t))
	reader := &delayedArtifact{entered: make(chan struct{}), release: make(chan struct{}), source: strings.NewReader("payload")}
	result := make(chan error, 1)
	go func() { _, err := artifacts.Upload(ctx, job, "report", reader, 7, connection); result <- err }()
	select {
	case <-reader.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = s.store.BeginRunnerConnection(ctx, s.runnerID)
	close(reader.release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("superseded artifact accepted")
	}
	arts, err := artifacts.ListForJob(ctx, job)
	if err != nil || len(arts) != 0 {
		t.Fatalf("old upload published: %v %v", arts, err)
	}
}

func TestArtifactReceiptExactReplay(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	job := s.job("refs/heads/main", "upload", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	artifacts := ci.NewArtifactStore(s.pool, artifactsBlobstore(t))
	first, err := artifacts.Upload(ctx, job, "report", strings.NewReader("payload"), 7, connection)
	if err != nil {
		t.Fatal(err)
	}
	again, err := artifacts.Upload(ctx, job, "report", strings.NewReader("payload"), 7, connection)
	if err != nil || again.ID != first.ID {
		t.Fatalf("identical replay changed receipt: %v", err)
	}
	if _, err := artifacts.Upload(ctx, job, "report", strings.NewReader("changed"), 7, connection); err == nil {
		t.Fatal("artifact overwrite accepted")
	}
	body, err := artifacts.Open(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil || string(raw) != "payload" {
		t.Fatal("accepted artifact corrupted")
	}
}
