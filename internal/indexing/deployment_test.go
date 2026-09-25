package indexing_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/redis/go-redis/v9"
)

func deploymentEvent() graph.DeploymentSuccessEvent {
	op := uuid.New()
	return graph.DeploymentSuccessEvent{OrgID: uuid.New(), RepoID: uuid.New(), OperationID: op, RunID: uuid.New(), EvidenceID: op.String() + ":1", LatestExecuteAttempt: 1, Artifact: "sha256:" + strings.Repeat("a", 64), Target: "staging", Destination: "cluster/ns/release", TargetRevision: "v1", ExternalID: "helm:2"}
}
func deploymentWorker(t *testing.T) context.Context {
	t.Helper()
	tok, err := svcauth.MintPlatform(testHMACSecret, graph.DeploymentEventWorker, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	name, err := svcauth.VerifyPlatform(testHMACSecret, tok)
	if err != nil {
		t.Fatal(err)
	}
	return authz.WithScope(t.Context(), authz.Scope{ActorKind: "service", PlatformWorker: name})
}
func TestDeploymentConsumerRejectsUnauthenticatedAndInvalidEvidence(t *testing.T) {
	s := graph.NewStore(graphPool(t))
	c := &indexing.DeploymentConsumer{Store: s, HMACSecret: testHMACSecret}
	e := deploymentEvent()
	data, _ := json.Marshal(e)
	if err := c.Handle(t.Context(), data); err == nil {
		t.Fatal("unauthenticated event accepted")
	}
	if err := c.Handle(scopedCtx(e.OrgID), data); err == nil {
		t.Fatal("ordinary org scope consumed cross-org stream")
	}
	worker := deploymentWorker(t)
	for _, raw := range [][]byte{[]byte(`{}`), append(append([]byte{}, data...), []byte(` {}`)...), []byte(strings.Replace(string(data), `"target":`, `"unexpected":1,"target":`, 1)), []byte(strings.Replace(string(data), `"latest_execute_attempt":1`, `"latest_execute_attempt":0`, 1)), []byte(strings.Replace(string(data), e.Artifact, "latest", 1)), make([]byte, (64<<10)+1)} {
		if err := c.Handle(worker, raw); err == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	noKey := &indexing.DeploymentConsumer{Store: s}
	if err := noKey.Handle(worker, data); err == nil {
		t.Fatal("org reentry without credential accepted")
	}
	if err := c.Handle(worker, data); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeRepository(scopedCtx(e.OrgID), e.RepoID); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(worker, data); err != nil {
		t.Fatalf("permanent deletion should acknowledge replay: %v", err)
	}
}

func TestDeploymentConsumerRedisPendingRestartReplayAndDeletion(t *testing.T) {
	if os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	stream := "stream:test:graph-deployment:" + uuid.NewString()
	defer rdb.Del(context.Background(), stream)
	s := graph.NewStore(graphPool(t))
	e := deploymentEvent()
	c := &indexing.DeploymentConsumer{Store: s, RDB: rdb, HMACSecret: testHMACSecret, Stream: stream}
	if err := events.EnsureGroup(t.Context(), rdb, stream, graph.DeploymentEventWorker); err != nil {
		t.Fatal(err)
	}
	if err := events.Publish(t.Context(), rdb, stream, e); err != nil {
		t.Fatal(err)
	}
	// Simulate the prior process crashing after delivery and before XACK. Run must
	// use the same durable consumer identity and handle this pending event first.
	if _, err := rdb.XReadGroup(t.Context(), &redis.XReadGroupArgs{Group: graph.DeploymentEventWorker, Consumer: graph.DeploymentEventWorker, Streams: []string{stream, ">"}, Count: 1, Block: -1}).Result(); err != nil {
		t.Fatal(err)
	}
	start := func() func() {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		return func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("consumer failed to stop")
			}
		}
	}
	wait := func(wantNodes int, lastID string) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			if err := s.Pool().QueryRow(t.Context(), `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2`, e.OrgID, e.RepoID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			pending, err := rdb.XPending(t.Context(), stream, graph.DeploymentEventWorker).Result()
			if err != nil {
				t.Fatal(err)
			}
			groups, err := rdb.XInfoGroups(t.Context(), stream).Result()
			if err != nil {
				t.Fatal(err)
			}
			if n == wantNodes && pending.Count == 0 && len(groups) == 1 && groups[0].LastDeliveredID == lastID {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("projection/ack not observed, wanted %d nodes", wantNodes)
	}
	latest := func() string {
		t.Helper()
		msgs, err := rdb.XRevRangeN(t.Context(), stream, "+", "-", 1).Result()
		if err != nil || len(msgs) != 1 {
			t.Fatalf("last event: %v", err)
		}
		return msgs[0].ID
	}
	stop := start()
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	wait(2, latest())
	if err := events.Publish(t.Context(), rdb, stream, e); err != nil {
		t.Fatal(err)
	}
	wait(2, latest())
	stop()
	stop = nil
	if err := s.PurgeRepository(scopedCtx(e.OrgID), e.RepoID); err != nil {
		t.Fatal(err)
	}
	if err := events.Publish(t.Context(), rdb, stream, e); err != nil {
		t.Fatal(err)
	}
	stop = start()
	wait(0, latest())
	// Organization deletion also fences events for repositories never seen before.
	if err := s.PurgeOrganization(scopedCtx(e.OrgID)); err != nil {
		t.Fatal(err)
	}
	// Use a new evidence identity: reusing the old operation/attempt for a
	// different repository is a conflict, even after deletion, and stays pending.
	deletedOrg := e.OrgID
	e = deploymentEvent()
	e.OrgID = deletedOrg
	if err := events.Publish(t.Context(), rdb, stream, e); err != nil {
		t.Fatal(err)
	}
	wait(0, latest())
}

func TestDeploymentPoisonDoesNotStarveAnotherOrganization(t *testing.T) {
	if os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	stream := "stream:test:graph-deployment-poison:" + uuid.NewString()
	defer rdb.Del(context.Background(), stream)
	store := graph.NewStore(graphPool(t))
	c := &indexing.DeploymentConsumer{Store: store, RDB: rdb, HMACSecret: testHMACSecret, Stream: stream}
	// A valid event ID with conflicting payload is permanent poison, not merely
	// bad JSON. Seed its original evidence, then leave the conflict pending as if
	// a previous process died. Another org's fresh event must still project.
	bad := deploymentEvent()
	data, _ := json.Marshal(bad)
	if err := c.Handle(deploymentWorker(t), data); err != nil {
		t.Fatal(err)
	}
	bad.ExternalID = "conflicting-observation"
	if err := events.EnsureGroup(t.Context(), rdb, stream, graph.DeploymentEventWorker); err != nil {
		t.Fatal(err)
	}
	if err := events.Publish(t.Context(), rdb, stream, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := rdb.XReadGroup(t.Context(), &redis.XReadGroupArgs{Group: graph.DeploymentEventWorker, Consumer: graph.DeploymentEventWorker, Streams: []string{stream, ">"}, Count: 1, Block: -1}).Result(); err != nil {
		t.Fatal(err)
	}
	good := deploymentEvent()
	if err := events.Publish(t.Context(), rdb, stream, good); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(4 * time.Second):
			t.Error("consumer did not stop")
		}
	}()
	for ctx.Err() == nil {
		var n int
		if err := store.Pool().QueryRow(t.Context(), `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2`, good.OrgID, good.RepoID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		pending, err := rdb.XPending(t.Context(), stream, graph.DeploymentEventWorker).Result()
		if err != nil {
			t.Fatal(err)
		}
		if n == 2 && pending.Count == 1 {
			var external string
			if err := store.Pool().QueryRow(t.Context(), `SELECT attrs->>'external_id' FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='deployment'`, bad.OrgID, bad.RepoID).Scan(&external); err != nil || external == bad.ExternalID {
				t.Fatalf("poison overwrote evidence: %q %v", external, err)
			}
			if n, err := rdb.XLen(t.Context(), stream).Result(); err != nil || n != 2 {
				t.Fatalf("event evidence discarded: %d %v", n, err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("poison event starved another organization's fresh event")
}
