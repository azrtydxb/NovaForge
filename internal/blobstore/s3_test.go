package blobstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/blobstore"
)

func newClient(t *testing.T) *blobstore.Client {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	c, err := blobstore.New(context.Background(), blobstore.Options{
		Endpoint:  ep,
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		Bucket:    "novaforge-test-" + uuid.NewString()[:8],
		UseSSL:    false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestPutGetRoundTrip(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	body := "hello"
	if err := c.Put(ctx, "a/b.txt", strings.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	r, err := c.Get(ctx, "a/b.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != body {
		t.Fatalf("want %q, got %q", body, got)
	}
}

func TestGetMissingKey(t *testing.T) {
	c := newClient(t)
	_, err := c.Get(context.Background(), "nope/missing.txt")
	if !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteRemovesObject(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	if err := c.Put(ctx, "x.txt", bytes.NewReader([]byte("z")), 1, "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Delete(ctx, "x.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := c.Get(ctx, "x.txt"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestBucketCreatedOnFirstBoot(t *testing.T) {
	// A fresh air-gapped install must not require an operator to pre-create
	// the bucket, so New creates it when absent.
	c := newClient(t)
	if err := c.Put(context.Background(), "k", strings.NewReader("v"), 1, "text/plain"); err != nil {
		t.Fatalf("Put into freshly created bucket: %v", err)
	}
}
