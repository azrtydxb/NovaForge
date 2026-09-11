package ci_test

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/ci"
)

func logsBlobstore(t *testing.T) *blobstore.Client {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	c, err := blobstore.New(context.Background(), blobstore.Options{
		Endpoint:  ep,
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		Bucket:    "novaforge-test-ci-logs",
		UseSSL:    false,
	})
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	return c
}

func TestTailReceivesAppendedLines(t *testing.T) {
	rdb := ciRedis(t)
	sink := ci.NewLogSink(rdb, nil)
	jobID := uuid.New()
	t.Cleanup(func() { rdb.Del(context.Background(), "joblog:"+jobID.String()) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tail, err := sink.Tail(ctx, jobID, "0")
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}

	want := []string{"line one", "line two", "line three"}
	go func() {
		for _, line := range want {
			time.Sleep(50 * time.Millisecond)
			if err := sink.Append(context.Background(), jobID, line); err != nil {
				t.Errorf("Append: %v", err)
			}
		}
	}()

	var got []string
	for len(got) < len(want) {
		select {
		case line := <-tail:
			got = append(got, line)
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for tailed lines, got %v so far", got)
		}
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("line %d: want %q, got %q", i, w, got[i])
		}
	}
}

func TestSealMovesLogToObjectStorage(t *testing.T) {
	rdb := ciRedis(t)
	blobs := logsBlobstore(t)
	sink := ci.NewLogSink(rdb, blobs)
	jobID := uuid.New()
	streamKey := "joblog:" + jobID.String()
	t.Cleanup(func() { rdb.Del(context.Background(), streamKey) })

	ctx := context.Background()
	if err := sink.Append(ctx, jobID, "first line"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := sink.Append(ctx, jobID, "second line"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	key, err := sink.Seal(ctx, jobID)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	r, err := blobs.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get sealed log: %v", err)
	}
	defer r.Close()
	content, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read sealed log: %v", err)
	}
	want := "first line\nsecond line\n"
	if string(content) != want {
		t.Fatalf("want sealed log %q, got %q", want, string(content))
	}

	exists, err := rdb.Exists(ctx, streamKey).Result()
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists != 0 {
		t.Fatalf("want redis stream %s deleted after sealing, still exists", streamKey)
	}
}
