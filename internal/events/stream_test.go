package events_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/events"
)

func redisClient(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	return redis.NewClient(opts)
}

func TestPublishAndConsume(t *testing.T) {
	rdb := redisClient(t)
	defer rdb.Close()
	ctx := context.Background()

	stream := "stream:test:push:" + uuid.NewString()
	group := "test-group"
	defer rdb.Del(ctx, stream)

	if err := events.EnsureGroup(ctx, rdb, stream, group); err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}

	want := events.PushEvent{
		OrgID:    uuid.New(),
		RepoID:   uuid.New(),
		PusherID: uuid.New(),
		Ref:      "refs/heads/main",
		OldSHA:   "0000000000000000000000000000000000000000",
		NewSHA:   "1111111111111111111111111111111111111111",
		At:       time.Now().UTC().Truncate(time.Second),
	}
	if err := events.Publish(ctx, rdb, stream, want); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: "test-consumer",
		Streams:  []string{stream, ">"},
		Count:    1,
		Block:    5 * time.Second,
	}).Result()
	if err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if len(res) != 1 || len(res[0].Messages) != 1 {
		t.Fatalf("want 1 message, got %+v", res)
	}
	raw, ok := res[0].Messages[0].Values["data"].(string)
	if !ok {
		t.Fatalf("want string field 'data', got %+v", res[0].Messages[0].Values)
	}
	var got events.PushEvent
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.NewSHA != want.NewSHA {
		t.Fatalf("want NewSHA %q, got %q", want.NewSHA, got.NewSHA)
	}
}

func TestEnsureGroupIdempotent(t *testing.T) {
	rdb := redisClient(t)
	defer rdb.Close()
	ctx := context.Background()

	stream := "stream:test:idempotent:" + uuid.NewString()
	group := "test-group"
	defer rdb.Del(ctx, stream)

	if err := events.EnsureGroup(ctx, rdb, stream, group); err != nil {
		t.Fatalf("first EnsureGroup: %v", err)
	}
	if err := events.EnsureGroup(ctx, rdb, stream, group); err != nil {
		t.Fatalf("second EnsureGroup should be nil, got: %v", err)
	}
}

func TestStreamGitPushConstant(t *testing.T) {
	if events.StreamGitPush != "stream:git:push" {
		t.Fatalf("want StreamGitPush == 'stream:git:push', got %q", events.StreamGitPush)
	}
}
