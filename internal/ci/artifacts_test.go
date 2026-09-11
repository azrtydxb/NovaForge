package ci_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/ci"
)

func artifactsBlobstore(t *testing.T) *blobstore.Client {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	c, err := blobstore.New(context.Background(), blobstore.Options{
		Endpoint:  ep,
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		Bucket:    "novaforge-test-ci-artifacts",
		UseSSL:    false,
	})
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	return c
}

// seedJob creates a run and one job on it, returning the job.
func seedJob(t *testing.T, store *ci.Store, orgID, repoID uuid.UUID) ci.WorkflowJob {
	t.Helper()
	ctx := scopedCtx(orgID)
	run, created, err := store.CreateRun(ctx, ci.Run{OrgID: orgID, RepoID: repoID, CommitSHA: uuid.NewString(), Ref: "refs/heads/main"})
	if err != nil || !created {
		t.Fatalf("CreateRun: run=%+v created=%v err=%v", run, created, err)
	}
	job, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "build", RunCmd: "go build ./..."})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return job
}

func TestUploadThenOpen(t *testing.T) {
	pool := ciPool(t)
	store := ci.NewStore(pool)
	blobs := artifactsBlobstore(t)
	artifacts := ci.NewArtifactStore(pool, blobs)

	orgID := uuid.New()
	repoID := uuid.New()
	job := seedJob(t, store, orgID, repoID)

	body := "hello world!!"
	art, err := artifacts.Upload(context.Background(), job.ID, "report.txt", strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	r, err := artifacts.Open(context.Background(), art.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Fatalf("want %q, got %q", body, string(got))
	}
}

func TestListIsScopedToRun(t *testing.T) {
	pool := ciPool(t)
	store := ci.NewStore(pool)
	blobs := artifactsBlobstore(t)
	artifacts := ci.NewArtifactStore(pool, blobs)

	orgID := uuid.New()
	repoID := uuid.New()
	jobA := seedJob(t, store, orgID, repoID)
	jobB := seedJob(t, store, orgID, repoID)

	if _, err := artifacts.Upload(context.Background(), jobA.ID, "a.txt", strings.NewReader("a"), 1); err != nil {
		t.Fatalf("Upload a: %v", err)
	}
	if _, err := artifacts.Upload(context.Background(), jobB.ID, "b.txt", strings.NewReader("b"), 1); err != nil {
		t.Fatalf("Upload b: %v", err)
	}

	list, err := artifacts.List(context.Background(), jobA.RunID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 artifact for jobA's run, got %d", len(list))
	}
	if list[0].Name != "a.txt" {
		t.Fatalf("want artifact a.txt, got %q", list[0].Name)
	}
}

func TestDuplicateArtifactNameRejected(t *testing.T) {
	pool := ciPool(t)
	store := ci.NewStore(pool)
	blobs := artifactsBlobstore(t)
	artifacts := ci.NewArtifactStore(pool, blobs)

	orgID := uuid.New()
	repoID := uuid.New()
	job := seedJob(t, store, orgID, repoID)

	if _, err := artifacts.Upload(context.Background(), job.ID, "dup.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	_, err := artifacts.Upload(context.Background(), job.ID, "dup.txt", strings.NewReader("y"), 1)
	if err == nil {
		t.Fatal("want error for duplicate artifact name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want error containing %q, got %q", "already exists", err.Error())
	}
}
