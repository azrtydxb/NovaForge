package events_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/events"
)

// TestConsumeRetriesAFailedDeletion pins what makes deletion events safe: a
// handler that fails leaves its message pending and gets it again, and a
// message is acknowledged only once handled. A consumer that acknowledged on
// receipt would lose a repository's cleanup to one database blip, silently —
// the rows would simply stay.
func TestConsumeRetriesAFailedDeletion(t *testing.T) {
	rdb := redisClient(t)
	defer rdb.Close()
	stream := "stream:test:deleted:" + uuid.NewString()
	defer rdb.Del(context.Background(), stream)

	want := events.RepoDeletedEvent{OrgID: uuid.New(), RepoID: uuid.New(), RepoName: "gone", At: time.Now().UTC()}
	if err := events.Publish(context.Background(), rdb, stream, want); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var calls atomic.Int32
	done := make(chan events.RepoDeletedEvent, 1)
	go func() {
		_ = events.Consume(ctx, rdb, stream, "cleanup", "c1", func(_ context.Context, data []byte) error {
			if calls.Add(1) == 1 {
				return errors.New("database briefly unavailable")
			}
			var got events.RepoDeletedEvent
			if err := events.Decode(data, &got); err != nil {
				return err
			}
			done <- got
			return nil
		})
	}()

	select {
	case got := <-done:
		if got.RepoID != want.RepoID || got.OrgID != want.OrgID {
			t.Fatalf("event = %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("a failed deletion was never retried")
	}
	// Acknowledged once handled: nothing is left pending for the group.
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, err := rdb.XPending(context.Background(), stream, "cleanup").Result()
		if err != nil {
			t.Fatal(err)
		}
		if p.Count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d message(s) still pending after the handler succeeded", p.Count)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("handler called %d times, want 2", n)
	}
}
