package ci

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/blobstore"
)

// logStreamMaxLen bounds each job's live-log Redis stream: XADD trims it
// approximately to this length so an unbounded job cannot grow the stream
// forever before it is sealed.
const logStreamMaxLen = 100_000

// tailPollInterval is how long each XREAD blocks between polls; Tail's
// caller stops it by cancelling ctx, which cannot interrupt a blocking XREAD
// directly, so the block is kept short to notice cancellation promptly.
const tailPollInterval = 2 * time.Second

// LogSink streams a job's live log lines through Redis while it runs, and
// seals the finished log into object storage.
type LogSink struct {
	rdb   *redis.Client
	blobs *blobstore.Client
}

// NewLogSink builds a LogSink backed by rdb for live tailing and blobs for
// sealed storage.
func NewLogSink(rdb *redis.Client, blobs *blobstore.Client) *LogSink {
	return &LogSink{rdb: rdb, blobs: blobs}
}

func logStreamKey(jobID uuid.UUID) string {
	return "joblog:" + jobID.String()
}

// sealedObjectKey returns the object storage key a sealed job log is
// written to.
func sealedObjectKey(jobID uuid.UUID) string {
	return "logs/" + jobID.String() + ".txt"
}

// Append adds one live log line for jobID, trimming the stream to
// approximately logStreamMaxLen entries.
func (s *LogSink) Append(ctx context.Context, jobID uuid.UUID, line string) error {
	err := s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: logStreamKey(jobID),
		MaxLen: logStreamMaxLen,
		Approx: true,
		Values: map[string]any{"line": line},
	}).Err()
	if err != nil {
		return fmt.Errorf("append log for job %s: %w", jobID, err)
	}
	return nil
}

// Tail streams jobID's log lines starting after id from (use "0" to read
// from the beginning) on the returned channel, which is closed when ctx is
// cancelled. It is a live tail: entries appended after Tail starts are
// delivered as they arrive, via a short-blocking XREAD loop.
func (s *LogSink) Tail(ctx context.Context, jobID uuid.UUID, from string) (<-chan string, error) {
	out := make(chan string)
	go func() {
		defer close(out)
		lastID := from
		if lastID == "" {
			lastID = "0"
		}
		for {
			if ctx.Err() != nil {
				return
			}
			res, err := s.rdb.XRead(ctx, &redis.XReadArgs{
				Streams: []string{logStreamKey(jobID), lastID},
				Block:   tailPollInterval,
				Count:   100,
			}).Result()
			if err != nil {
				if errors.Is(err, redis.Nil) || ctx.Err() != nil {
					continue
				}
				return
			}
			for _, stream := range res {
				for _, msg := range stream.Messages {
					line, _ := msg.Values["line"].(string)
					select {
					case out <- line:
					case <-ctx.Done():
						return
					}
					lastID = msg.ID
				}
			}
		}
	}()
	return out, nil
}

// Seal reads jobID's entire live log, writes it to object storage as
// logs/<jobID>.txt, and deletes the Redis stream. The returned key is the
// object storage key the sealed log now lives at.
func (s *LogSink) Seal(ctx context.Context, jobID uuid.UUID) (string, error) {
	key := logStreamKey(jobID)
	res, err := s.rdb.XRange(ctx, key, "-", "+").Result()
	if err != nil {
		return "", fmt.Errorf("read log for job %s: %w", jobID, err)
	}

	var b strings.Builder
	for _, msg := range res {
		line, _ := msg.Values["line"].(string)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	content := b.String()

	objectKey := sealedObjectKey(jobID)
	if err := s.blobs.Put(ctx, objectKey, strings.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		return "", fmt.Errorf("seal log for job %s: %w", jobID, err)
	}
	if err := s.rdb.Del(ctx, key).Err(); err != nil {
		return "", fmt.Errorf("delete live log for job %s after sealing: %w", jobID, err)
	}
	return objectKey, nil
}
