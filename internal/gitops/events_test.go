package gitops_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gitops"
)

func TestPushOverHTTPPublishesEvent(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	ctx := context.Background()

	group := "test-events-group"
	defer rdb.Del(ctx, events.StreamGitPush)
	if err := events.EnsureGroup(ctx, rdb, events.StreamGitPush, group); err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}

	gitops.WireRedisPushEvents(rdb)
	t.Cleanup(func() { gitops.PushPublisher = nil })

	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, "eventy"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	actorID := uuid.New()
	auth := func(ctx context.Context, user, pass string) (authz.Scope, error) {
		return authz.Scope{OrgID: orgID, ActorID: actorID, ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		return nil
	}
	handler := gitops.NewHTTPHandler(root, auth, caps)
	server := httptest.NewServer(handler)
	defer server.Close()

	repoURL := fmt.Sprintf("http://user:token@%s/%s/eventy.git", server.Listener.Addr().String(), orgID.String())
	work := t.TempDir()
	runCmd(t, "", "git", "clone", repoURL, work)
	writeFileHTTP(t, work+"/f.txt", "x\n")
	runCmd(t, work, "git", "add", "f.txt")
	runCmd(t, work, "git", "-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "push event test")
	runCmd(t, work, "git", "push", "origin", "HEAD:refs/heads/main")

	res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: "test-consumer",
		Streams:  []string{events.StreamGitPush, ">"},
		Count:    1,
		Block:    5 * time.Second,
	}).Result()
	if err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if len(res) != 1 || len(res[0].Messages) != 1 {
		t.Fatalf("want 1 push event, got %+v", res)
	}
	raw := res[0].Messages[0].Values["data"].(string)
	var evt events.PushEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Ref != "refs/heads/main" {
		t.Fatalf("want ref refs/heads/main, got %q", evt.Ref)
	}
	if evt.PusherID != actorID {
		t.Fatalf("want pusher id %s, got %s", actorID, evt.PusherID)
	}
	if evt.OrgID != orgID {
		t.Fatalf("want org id %s, got %s", orgID, evt.OrgID)
	}
}
