package webhooks_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/webhooks"
	"github.com/redis/go-redis/v9"
)

func TestRunAndCIWebhookOutbox(t *testing.T) {
	pool := webhooksPool(t)
	rdb := testRedis(t)
	for _, owner := range []string{"reviews", "ci"} {
		fs := reviews.MigrationsFS
		if owner == "ci" {
			fs = ci.MigrationsFS
		}
		if err := database.Migrate(suite.url, owner, fs); err != nil {
			t.Fatal(err)
		}
	}
	org := uuid.New()
	repo := mustRepoRow(t, pool, org)
	ctx := personIn(org)
	store := webhooks.NewStore(pool, testKEK)
	var mu sync.Mutex
	received := map[string]int{}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if webhooks.Sign(body, "secret") != r.Header.Get("X-NovaForge-Signature") {
			t.Error("bad domain signature")
		}
		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			t.Error(err)
		}
		mu.Lock()
		received[r.Header.Get("X-NovaForge-Event")]++
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	if _, err := store.CreateHook(ctx, repo, receiver.URL, []string{"engineering_run", "ci_result"}, "secret"); err != nil {
		t.Fatal(err)
	}
	stream := testStream(t, rdb)
	startWorker(t, &webhooks.Worker{RDB: rdb, Store: store, PushStream: testStream(t, rdb), DomainStreams: []string{stream}, Backoff: time.Millisecond})
	_, err := reviews.NewStore(pool).CreateRun(ctx, reviews.Run{OrgID: org, RepoID: repo, Title: "webhook", SourceRef: "feature", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := ci.NewStore(pool).CreateRun(ctx, ci.Run{OrgID: org, RepoID: repo, RepoName: "hooked", CommitSHA: "abc", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE ci.workflow_runs SET status='cancelled' WHERE id=$1", run.ID); err != nil {
		t.Fatal(err)
	}
	platform := authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "test-relay"})
	// Refusing a real connection leaves committed intent available to a later
	// healthy relay; no fake Redis acknowledgement can make this test pass.
	down := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	defer down.Close()
	if _, err := (events.Outbox{Pool: pool, Redis: down, Schema: "reviews", Stream: stream}).RelayOne(platform); err == nil {
		t.Fatal("unreachable Redis reported publication")
	}
	for _, owner := range []string{"reviews", "ci"} {
		relay := events.Outbox{Pool: pool, Redis: rdb, Schema: owner, Stream: stream}
		if _, err := relay.RelayOne(ctx); err == nil {
			t.Fatal("ordinary user could relay cross-org events")
		}
		for {
			more, err := relay.RelayOne(platform)
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				break
			}
		}
	}
	waitFor(t, "signed run and CI notifications", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return received["engineering_run"] == 1 && received["ci_result"] == 1
	})
	rows, err := rdb.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	var replayID string
	for _, row := range rows {
		replayID, err = rdb.XAdd(context.Background(), &redis.XAddArgs{Stream: stream, Values: row.Values}).Result()
		if err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "duplicate events acknowledged", func() bool {
		groups, e := rdb.XInfoGroups(context.Background(), stream).Result()
		return e == nil && len(groups) == 1 && groups[0].LastDeliveredID == replayID && groups[0].Pending == 0
	})
	mu.Lock()
	for event, n := range received {
		if n != 1 {
			t.Errorf("%s was delivered %d times after acknowledgement", event, n)
		}
	}
	mu.Unlock()
	// A rolled-back state transition must not create a phantom notification.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE ci.workflow_runs SET status='failure' WHERE id=$1", run.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if more, err := (events.Outbox{Pool: pool, Redis: rdb, Schema: "ci", Stream: stream}).RelayOne(platform); err != nil || more {
		t.Fatalf("rolled-back event published: %v %v", more, err)
	}
}

func TestWebhookRetryBudgetSurvivesRestart(t *testing.T) {
	pool := webhooksPool(t)
	rdb := testRedis(t)
	store := webhooks.NewStore(pool, testKEK)
	org := uuid.New()
	repo := mustRepoRow(t, pool, org)
	stream := testStream(t, rdb)
	var mu sync.Mutex
	attempts := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); attempts++; mu.Unlock(); w.WriteHeader(503) }))
	defer receiver.Close()
	if _, err := store.CreateHook(personIn(org), repo, receiver.URL, nil, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	worker := &webhooks.Worker{RDB: rdb, Store: store, PushStream: stream, MaxAttempts: 3, Backoff: time.Millisecond}
	go func() { done <- worker.Run(ctx) }()
	event := events.RepositoryEvent{Version: 1, EventID: uuid.New(), Event: "ci_result", OrgID: org, RepoID: repo, RunID: uuid.New(), State: "failure", At: time.Now().UTC()}
	if err := events.Publish(context.Background(), rdb, stream, event); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "three attempts settled", func() bool {
		mu.Lock()
		n := attempts
		mu.Unlock()
		p, e := rdb.XPending(context.Background(), stream, webhooks.ConsumerGroup).Result()
		return n == 3 && e == nil && p.Count == 0
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	startWorker(t, &webhooks.Worker{RDB: rdb, Store: store, PushStream: stream, MaxAttempts: 3, Backoff: time.Millisecond})
	if err := events.Publish(context.Background(), rdb, stream, event); err != nil {
		t.Fatal(err)
	}
	rows, err := rdb.XRevRangeN(context.Background(), stream, "+", "-", 1).Result()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replayed terminal delivery acknowledged", func() bool {
		groups, e := rdb.XInfoGroups(context.Background(), stream).Result()
		return e == nil && len(groups) == 1 && groups[0].LastDeliveredID == rows[0].ID && groups[0].Pending == 0
	})
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Fatalf("restart reset retry budget: %d HTTP attempts", attempts)
	}
}
