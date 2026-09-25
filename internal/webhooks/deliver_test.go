package webhooks_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/webhooks"
)

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatalf("parse TEST_REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// testStream is a stream of this test's own. The production stream is shared
// with every other consumer on the cluster's dev Redis, and a test that read it
// would both steal other work's events and see events it never published.
func testStream(t *testing.T, rdb *redis.Client) string {
	t.Helper()
	stream := "stream:test:webhooks:" + uuid.NewString()
	t.Cleanup(func() { rdb.Del(context.Background(), stream) })
	return stream
}

// startWorker runs w until the test ends, and fails the test if Run returns an
// error. A worker whose Run failed at once would otherwise look exactly like a
// worker that was simply never reached by an event.
func startWorker(t *testing.T, w *webhooks.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := w.Run(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("worker Run: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func pushEvent(orgID, repoID uuid.UUID) events.PushEvent {
	return events.PushEvent{
		OrgID: orgID, RepoID: repoID, RepoName: "hooked",
		PusherID: uuid.New(), PusherKind: "user",
		Ref: "refs/heads/main", OldSHA: "0000000000000000000000000000000000000000",
		NewSHA: "1111111111111111111111111111111111111111", At: time.Now().UTC(),
	}
}

// waitFor polls until cond holds, so a test never sleeps for a fixed guess at
// how long delivery takes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestWebhookDelivered is the whole point of the feature: a push reaches the
// endpoint someone registered, signed with the secret only they and this platform
// hold, and the platform keeps a record of what the endpoint answered.
func TestWebhookDelivered(t *testing.T) {
	pool := webhooksPool(t)
	rdb := testRedis(t)
	store := webhooks.NewStore(pool, testKEK)

	orgID := uuid.New()
	repoID := mustRepoRow(t, pool, orgID)
	secret := "push-secret-" + uuid.NewString()

	var mu sync.Mutex
	var body []byte
	var signature, event string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		body, signature, event = raw, r.Header.Get("X-NovaForge-Signature"), r.Header.Get("X-NovaForge-Event")
		method := r.Method
		mu.Unlock()
		if method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	hook, err := store.CreateHook(personIn(orgID), repoID, receiver.URL, []string{"push"}, secret)
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}

	stream := testStream(t, rdb)
	startWorker(t, &webhooks.Worker{
		RDB: rdb, Store: store, PushStream: stream,
		Consumer: "test-" + uuid.NewString(), MaxAttempts: 3, Backoff: time.Millisecond,
	})

	if err := events.Publish(context.Background(), rdb, stream, pushEvent(orgID, repoID)); err != nil {
		t.Fatalf("publish push: %v", err)
	}

	waitFor(t, "the receiver to be posted to", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return body != nil
	})
	mu.Lock()
	gotBody, gotSig, gotEvent := body, signature, event
	mu.Unlock()

	if gotEvent != "push" {
		t.Errorf("X-NovaForge-Event = %q, want push", gotEvent)
	}
	// The signature covers the exact bytes the receiver read. Signing anything
	// else — a re-marshalled copy, the payload before the envelope was added —
	// is a signature the receiver cannot reproduce.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	if want := hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Errorf("X-NovaForge-Signature = %q, want HMAC-SHA256 of the body %q", gotSig, want)
	}

	var deliveries []webhooks.Delivery
	waitFor(t, "the delivery to be recorded", func() bool {
		d, err := store.ListDeliveries(personIn(orgID), hook.ID, 10)
		if err != nil {
			t.Fatalf("ListDeliveries: %v", err)
		}
		deliveries = d
		return len(d) > 0
	})
	if len(deliveries) != 1 {
		t.Fatalf("one push recorded %d deliveries: %+v", len(deliveries), deliveries)
	}
	got := deliveries[0]
	if got.StatusCode != http.StatusOK || got.Attempt != 1 || got.Event != "push" || got.Error != "" {
		t.Errorf("delivery = %+v, want status 200 on attempt 1 of push with no error", got)
	}
}

// TestWebhookRetriesAreBounded: an endpoint that is simply broken must not become
// a permanent load on the platform. The worker tries it a bounded number of times
// and then leaves the delivery failed — it does not retry forever, and it does not
// leave the stream message pending so that every restart tries it again.
func TestWebhookRetriesAreBounded(t *testing.T) {
	pool := webhooksPool(t)
	rdb := testRedis(t)
	store := webhooks.NewStore(pool, testKEK)

	orgID := uuid.New()
	repoID := mustRepoRow(t, pool, orgID)

	var mu sync.Mutex
	attempts := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()

	hook, err := store.CreateHook(personIn(orgID), repoID, receiver.URL, nil, "s")
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}

	const bound = 3
	stream := testStream(t, rdb)
	startWorker(t, &webhooks.Worker{
		RDB: rdb, Store: store, PushStream: stream,
		Consumer: "test-" + uuid.NewString(), MaxAttempts: bound, Backoff: time.Millisecond,
	})
	if err := events.Publish(context.Background(), rdb, stream, pushEvent(orgID, repoID)); err != nil {
		t.Fatalf("publish push: %v", err)
	}

	var deliveries []webhooks.Delivery
	waitFor(t, "the attempts to reach the bound", func() bool {
		d, err := store.ListDeliveries(personIn(orgID), hook.ID, 20)
		if err != nil {
			t.Fatalf("ListDeliveries: %v", err)
		}
		deliveries = d
		return len(d) >= bound
	})

	// Give a worker that had not given up the chance to prove it: if retries were
	// unbounded, more attempts arrive during this window.
	time.Sleep(2 * time.Second)
	mu.Lock()
	tried := attempts
	mu.Unlock()
	if tried != bound {
		t.Errorf("the receiver was posted to %d times, want exactly the bound of %d", tried, bound)
	}
	deliveries, err = store.ListDeliveries(personIn(orgID), hook.ID, 20)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(deliveries) != bound {
		t.Fatalf("recorded %d deliveries, want %d: %+v", len(deliveries), bound, deliveries)
	}
	// ListDeliveries is newest first, so the last attempt is the first row: it is
	// left failed rather than deleted, because "it never worked" is the single
	// most useful thing the history can tell whoever registered the hook.
	last := deliveries[0]
	if last.Attempt != bound || last.StatusCode != http.StatusInternalServerError {
		t.Errorf("final delivery = %+v, want attempt %d with status 500", last, bound)
	}

	// Nothing is left pending: a message the worker gave up on has been
	// acknowledged, or every restart of the service would deliver it again.
	pending, err := rdb.XPending(context.Background(), stream, webhooks.ConsumerGroup).Result()
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	if pending.Count != 0 {
		t.Errorf("%d messages left pending after giving up; a restart would retry forever", pending.Count)
	}
}

// TestTwoReplicasDeliverOnce is why the worker is a consumer group and not a
// plain XREAD: git-platform runs with more than one replica, and a hook that
// fired once per replica would double every notification the platform sends.
func TestTwoReplicasDeliverOnce(t *testing.T) {
	pool := webhooksPool(t)
	rdb := testRedis(t)
	store := webhooks.NewStore(pool, testKEK)

	orgID := uuid.New()
	repoID := mustRepoRow(t, pool, orgID)

	var mu sync.Mutex
	posts := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	hook, err := store.CreateHook(personIn(orgID), repoID, receiver.URL, nil, "s")
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}

	stream := testStream(t, rdb)
	for _, replica := range []string{"replica-a", "replica-b"} {
		startWorker(t, &webhooks.Worker{
			RDB: rdb, Store: store, PushStream: stream,
			Consumer: replica + "-" + uuid.NewString(), MaxAttempts: 2, Backoff: time.Millisecond,
		})
	}
	if err := events.Publish(context.Background(), rdb, stream, pushEvent(orgID, repoID)); err != nil {
		t.Fatalf("publish push: %v", err)
	}

	waitFor(t, "the delivery", func() bool {
		d, err := store.ListDeliveries(personIn(orgID), hook.ID, 10)
		if err != nil {
			t.Fatalf("ListDeliveries: %v", err)
		}
		return len(d) > 0
	})
	// Both replicas are reading the same stream; if they were not sharing a
	// consumer group, the second post arrives within this window.
	time.Sleep(2 * time.Second)
	mu.Lock()
	got := posts
	mu.Unlock()
	if got != 1 {
		t.Errorf("two replicas posted %d times for one push, want 1", got)
	}
}

// TestHookEventFilterIsRespected: a hook that asked only for a different event is
// not delivered a push. An empty list means every event, which is the reading that
// makes a hook registered without a filter useful rather than silent.
func TestHookEventFilterIsRespected(t *testing.T) {
	pool := webhooksPool(t)
	rdb := testRedis(t)
	store := webhooks.NewStore(pool, testKEK)

	orgID := uuid.New()
	repoID := mustRepoRow(t, pool, orgID)

	var mu sync.Mutex
	posts := map[string]int{}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	ctx := personIn(orgID)
	if _, err := store.CreateHook(ctx, repoID, receiver.URL+"/wanted", []string{"push"}, ""); err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if _, err := store.CreateHook(ctx, repoID, receiver.URL+"/all", nil, ""); err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if _, err := store.CreateHook(ctx, repoID, receiver.URL+"/unwanted", []string{"release"}, ""); err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	// An inactive hook is off, not merely unsubscribed.
	off, err := store.CreateHook(ctx, repoID, receiver.URL+"/off", []string{"push"}, "")
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if err := store.SetActive(ctx, off.ID, false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	stream := testStream(t, rdb)
	startWorker(t, &webhooks.Worker{
		RDB: rdb, Store: store, PushStream: stream,
		Consumer: "test-" + uuid.NewString(), MaxAttempts: 2, Backoff: time.Millisecond,
	})
	if err := events.Publish(context.Background(), rdb, stream, pushEvent(orgID, repoID)); err != nil {
		t.Fatalf("publish push: %v", err)
	}

	waitFor(t, "both subscribed hooks to fire", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return posts["/wanted"] == 1 && posts["/all"] == 1
	})
	time.Sleep(time.Second)
	mu.Lock()
	defer mu.Unlock()
	if posts["/unwanted"] != 0 {
		t.Errorf("a hook subscribed to release was sent %d pushes", posts["/unwanted"])
	}
	if posts["/off"] != 0 {
		t.Errorf("an inactive hook was sent %d pushes", posts["/off"])
	}
}
