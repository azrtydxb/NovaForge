package ci_test

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

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
		Bucket:    "novaforge-test-ci-logs-" + uuid.NewString()[:8],
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
	// The producer reports through appendErr instead of t.Errorf, and the test
	// waits for it before returning: a goroutine that touches t after the test
	// ends panics the whole package, and the cleanup closing rdb would make
	// any still-running Append fail.
	appendErr := make(chan error, 1)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for _, line := range want {
			if err := sink.Append(context.Background(), jobID, line); err != nil {
				appendErr <- err
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-producerDone
	})

	var got []string
	timeout := time.After(30 * time.Second)
	for len(got) < len(want) {
		select {
		case line, ok := <-tail:
			if !ok {
				t.Fatalf("tail closed before cancel, got %v so far", got)
			}
			got = append(got, line)
		case err := <-appendErr:
			t.Fatalf("Append: %v", err)
		case <-timeout:
			t.Fatalf("timed out waiting for tailed lines, got %v so far", got)
		}
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("line %d: want %q, got %q", i, w, got[i])
		}
	}
}

// failFirstXRead makes the first XREAD fail the way a timed-out or dropped
// connection does, then lets everything through.
type failFirstXRead struct{ failed atomic.Bool }

func (h *failFirstXRead) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *failFirstXRead) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *failFirstXRead) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "xread" && h.failed.CompareAndSwap(false, true) {
			return errors.New("simulated redis i/o timeout")
		}
		return next(ctx, cmd)
	}
}

func TestTailSurvivesRedisError(t *testing.T) {
	rdb := ciRedis(t)
	rdb.AddHook(&failFirstXRead{})
	sink := ci.NewLogSink(rdb, nil)
	jobID := uuid.New()
	t.Cleanup(func() { rdb.Del(context.Background(), "joblog:"+jobID.String()) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tail, err := sink.Tail(ctx, jobID, "0")
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if err := sink.Append(ctx, jobID, "after the error"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	select {
	case line, ok := <-tail:
		if !ok {
			t.Fatal("tail closed on a transient redis error")
		}
		if line != "after the error" {
			t.Fatalf("got %q", line)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("tail never recovered from the redis error")
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
