// Package events publishes and consumes NovaForge domain events on Redis
// Streams. Redis durability is AOF persistence with consumer-group
// redelivery, which gives at-least-once delivery: every stream handler must
// therefore be idempotent, since exactly-once delivery is never assumed.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// StreamGitPush is the stream name push events are published to.
const StreamGitPush = "stream:git:push"

// PushEvent describes one ref update accepted by a git push.
type PushEvent struct {
	OrgID    uuid.UUID `json:"org_id"`
	RepoID   uuid.UUID `json:"repo_id"`
	PusherID uuid.UUID `json:"pusher_id"`
	Ref      string    `json:"ref"`
	OldSHA   string    `json:"old_sha"`
	NewSHA   string    `json:"new_sha"`
	At       time.Time `json:"at"`
}

// Publish marshals payload to JSON and adds it to stream in the "data"
// field via XADD.
func Publish(ctx context.Context, rdb *redis.Client, stream string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: map[string]any{"data": string(data)},
	}).Err(); err != nil {
		return fmt.Errorf("publish to stream %s: %w", stream, err)
	}
	return nil
}

// EnsureGroup creates group on stream if it does not already exist,
// creating the stream itself when needed. It is idempotent: calling it
// again for a group that already exists returns nil rather than the
// BUSYGROUP error Redis reports.
func EnsureGroup(ctx context.Context, rdb *redis.Client, stream, group string) error {
	err := rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("ensure consumer group %s on stream %s: %w", group, stream, err)
	}
	return nil
}
