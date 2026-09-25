package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// StreamRepoDeleted carries one RepoDeletedEvent per repository git-platform
// is deleting. Every service holding rows keyed on the repository consumes it
// and deletes its own: one schema per service, so no service deletes another's.
const StreamRepoDeleted = "stream:git:repo-deleted"

// StreamOrgDeleted carries one OrgDeletedEvent per organization identity is
// deleting. Every service consumes it and removes that organization's data
// from its own schema, object storage and disk.
const StreamOrgDeleted = "stream:identity:org-deleted"

// StreamRunsDeleted carries the ids of runs a service deleted, for the
// services that keep rows keyed on those runs without knowing their
// repository — gate evaluations, approvals and credential leases.
const StreamRunsDeleted = "stream:runs:deleted"

// RepoDeletedEvent says a repository is being deleted.
type RepoDeletedEvent struct {
	OrgID    uuid.UUID `json:"org_id"`
	RepoID   uuid.UUID `json:"repo_id"`
	RepoName string    `json:"repo_name"`
	At       time.Time `json:"at"`
}

// OrgDeletedEvent says an organization is being deleted.
type OrgDeletedEvent struct {
	OrgID   uuid.UUID `json:"org_id"`
	OrgName string    `json:"org_name"`
	At      time.Time `json:"at"`
}

// RunsDeletedEvent names runs one service deleted. Kind is "engineering" for
// Engineering Runs and "agent" for Agent Runs.
type RunsDeletedEvent struct {
	OrgID  uuid.UUID   `json:"org_id"`
	Kind   string      `json:"kind"`
	RunIDs []uuid.UUID `json:"run_ids"`
	At     time.Time   `json:"at"`
}

// consumeRetry is how long a failed message waits before it is handled again.
const consumeRetry = 5 * time.Second

// Consume reads stream as consumer in group until ctx is cancelled, passing
// each message's data to handle and acknowledging it only when handle
// succeeds. A message whose handler fails stays pending and is handed back on
// a later bounded pass, so a deletion that failed half-way — a database blip, an
// object store that was briefly down — is finished later rather than lost.
// Handlers must therefore be idempotent, which deletions are.
func Consume(ctx context.Context, rdb *redis.Client, stream, group, consumer string, handle func(ctx context.Context, data []byte) error) error {
	if err := EnsureGroup(ctx, rdb, stream, group); err != nil {
		return err
	}
	// Rotate through pending IDs rather than retrying only the first page. Each
	// pass then admits one bounded fresh batch even if old handlers keep failing.
	// Handling remains sequential within each batch; failed messages stay pending.
	pendingAfter := "0"
	var pendingRemaining int64
	for ctx.Err() == nil {
		if pendingAfter == "0" {
			// A finite pass must not chase a tail of newly failing fresh messages
			// forever. The group's current count bounds this consumer's older work;
			// another consumer's entries may increase the bound but cannot erase it.
			snapshot, err := rdb.XPending(ctx, stream, group).Result()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				log.Printf("events: pending inventory %s: %v", stream, err)
				sleep(ctx, consumeRetry)
				continue
			}
			pendingRemaining = snapshot.Count
		}
		pending, err := read(ctx, rdb, stream, group, consumer, pendingAfter, 0)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("events: read pending %s: %v", stream, err)
			sleep(ctx, consumeRetry)
			continue
		}
		pendingRemaining -= int64(len(pending))
		if len(pending) > 0 && pendingRemaining > 0 {
			pendingAfter = pending[len(pending)-1].ID
		} else {
			pendingAfter = "0"
		}
		failed := false
		handleBatch := func(msgs []redis.XMessage) {
			for _, m := range msgs {
				if ctx.Err() != nil {
					return
				}
				data, _ := m.Values["data"].(string)
				if err := handle(ctx, []byte(data)); err != nil {
					log.Printf("events: %s %s on %s: %v (will retry)", group, m.ID, stream, err)
					failed = true
					continue
				}
				if err := rdb.XAck(ctx, stream, group, m.ID).Err(); err != nil {
					log.Printf("events: ack %s on %s: %v", m.ID, stream, err)
				}
			}
		}
		handleBatch(pending)
		// Do not block the pending scan for idle fresh traffic; otherwise a large
		// backlog would accrue a two-second delay per page even after recovery.
		block := 2 * time.Second
		if len(pending) > 0 {
			block = -1
		}
		fresh, err := read(ctx, rdb, stream, group, consumer, ">", block)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("events: read %s: %v", stream, err)
			failed = true
		} else {
			handleBatch(fresh)
		}
		if failed {
			sleep(ctx, consumeRetry)
		}
	}

	return ctx.Err()
}

func read(ctx context.Context, rdb *redis.Client, stream, group, consumer, id string, block time.Duration) ([]redis.XMessage, error) {
	args := &redis.XReadGroupArgs{
		Group: group, Consumer: consumer, Streams: []string{stream, id}, Count: 16,
	}
	if id == ">" {
		args.Block = block
	} else {
		// A negative Block omits BLOCK, so a pending read returns at once.
		args.Block = -1
	}
	res, err := rdb.XReadGroup(ctx, args).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []redis.XMessage
	for _, s := range res {
		out = append(out, s.Messages...)
	}
	return out, nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// Decode unmarshals one event's data, naming the stream in its error.
func Decode(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode event: %w", err)
	}
	return nil
}
