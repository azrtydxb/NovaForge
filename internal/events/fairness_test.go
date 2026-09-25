package events_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/redis/go-redis/v9"
)

func TestConsumeFairPendingAndFreshWithoutDroppingFailures(t *testing.T) {
	rdb := redisClient(t)
	defer rdb.Close()
	stream := "stream:test:fair:" + uuid.NewString()
	defer rdb.Del(context.Background(), stream)
	const group, consumer = "fair-test", "worker"
	if err := events.EnsureGroup(t.Context(), rdb, stream, group); err != nil {
		t.Fatal(err)
	}
	for range 16 {
		if err := events.Publish(t.Context(), rdb, stream, "poison"); err != nil {
			t.Fatal(err)
		}
	}
	if err := events.Publish(t.Context(), rdb, stream, "pending-good"); err != nil {
		t.Fatal(err)
	}
	if _, err := rdb.XReadGroup(t.Context(), &redis.XReadGroupArgs{Group: group, Consumer: consumer, Streams: []string{stream, ">"}, Count: 17, Block: -1}).Result(); err != nil {
		t.Fatal(err)
	}
	if err := events.Publish(t.Context(), rdb, stream, "fresh-good"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	good := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- events.Consume(ctx, rdb, stream, group, consumer, func(_ context.Context, data []byte) error {
			if strings.Contains(string(data), "poison") {
				return errors.New("permanent poison")
			}
			good <- string(data)
			return nil
		})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("consumer did not stop")
		}
	}()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case value := <-good:
			seen[value] = true
		case <-ctx.Done():
			t.Fatalf("poison blocked pending/fresh messages: %v", seen)
		}
	}
	if !seen[`"pending-good"`] || !seen[`"fresh-good"`] {
		t.Fatalf("wrong messages: %v", seen)
	}
	deadline := time.Now().Add(time.Second)
	for {
		p, err := rdb.XPending(t.Context(), stream, group).Result()
		if err != nil {
			t.Fatal(err)
		}
		if p.Count == 16 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failures lost or success not acknowledged: %d", p.Count)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n, err := rdb.XLen(t.Context(), stream).Result(); err != nil || n != 18 {
		t.Fatalf("stream evidence discarded: %d %v", n, err)
	}
}

func TestConsumeRetriesOldFailureDespiteContinuousFreshFailures(t *testing.T) {
	rdb := redisClient(t)
	defer rdb.Close()
	stream := "stream:test:fair-tail:" + uuid.NewString()
	defer rdb.Del(context.Background(), stream)
	const group, consumer = "fair-tail", "worker"
	if err := events.EnsureGroup(t.Context(), rdb, stream, group); err != nil {
		t.Fatal(err)
	}
	if err := events.Publish(t.Context(), rdb, stream, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := rdb.XReadGroup(t.Context(), &redis.XReadGroupArgs{Group: group, Consumer: consumer, Streams: []string{stream, ">"}, Count: 1, Block: -1}).Result(); err != nil {
		t.Fatal(err)
	}
	for range 16 {
		if err := events.Publish(t.Context(), rdb, stream, "new"); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	done := make(chan error, 1)
	retried := make(chan struct{}, 1)
	oldCalls := 0
	go func() {
		done <- events.Consume(ctx, rdb, stream, group, consumer, func(call context.Context, data []byte) error {
			if string(data) == `"old"` {
				oldCalls++
				if oldCalls == 2 {
					retried <- struct{}{}
					return nil
				}
				return errors.New("temporary old failure")
			}
			// Keep the pending tail growing at least as fast as the pending scan.
			if err := events.Publish(call, rdb, stream, "new"); err != nil {
				return err
			}
			return errors.New("new failure")
		})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("consumer did not stop")
		}
	}()
	select {
	case <-retried:
	case <-ctx.Done():
		t.Fatal("moving pending tail starved retry of old failure")
	}
}

func TestConsumePreservesSequentialHandlingAndSuccessOrder(t *testing.T) {
	rdb := redisClient(t)
	defer rdb.Close()
	stream := "stream:test:fair-order:" + uuid.NewString()
	defer rdb.Del(context.Background(), stream)
	for _, value := range []string{"first", "second", "third"} {
		if err := events.Publish(t.Context(), rdb, stream, value); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	seen := make(chan string, 3)
	var active atomic.Bool
	go func() {
		done <- events.Consume(ctx, rdb, stream, "ordered-batch", "worker", func(_ context.Context, data []byte) error {
			if !active.CompareAndSwap(false, true) {
				t.Error("handler invoked concurrently")
			}
			defer active.Store(false)
			seen <- string(data)
			return nil
		})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("consumer did not stop")
		}
	}()
	for _, want := range []string{`"first"`, `"second"`, `"third"`} {
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("sequential batch order: got %s want %s", got, want)
			}
		case <-ctx.Done():
			t.Fatal("ordered messages missing")
		}
	}
	for ctx.Err() == nil {
		p, err := rdb.XPending(t.Context(), stream, "ordered-batch").Result()
		if err != nil {
			t.Fatal(err)
		}
		if p.Count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("successful messages were not acknowledged")
}
