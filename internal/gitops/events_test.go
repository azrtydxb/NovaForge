package gitops_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
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
	auth := func(ctx context.Context, user, pass, orgRef string) (authz.Scope, error) {
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

	// stream:git:push is shared and durable, so an event left by an earlier run
	// would be read first. Scan for the event this test actually produced
	// instead of assuming the stream is empty.
	pushed := strings.TrimSpace(runCmdOut(t, work, "git", "rev-parse", "HEAD"))
	var evt events.PushEvent
	deadline := time.Now().Add(15 * time.Second)
	for evt.NewSHA != pushed {
		if time.Now().After(deadline) {
			t.Fatalf("no push event for %s arrived within 15s", pushed)
		}
		res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: "test-consumer",
			Streams:  []string{events.StreamGitPush, ">"},
			Count:    10,
			Block:    2 * time.Second,
		}).Result()
		if err != nil && err != redis.Nil {
			t.Fatalf("XReadGroup: %v", err)
		}
		for _, st := range res {
			for _, m := range st.Messages {
				var candidate events.PushEvent
				if err := json.Unmarshal([]byte(m.Values["data"].(string)), &candidate); err != nil {
					continue
				}
				if candidate.NewSHA == pushed {
					evt = candidate
				}
			}
		}
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

// runCmdOut runs a command and returns its stdout, failing the test on error.
func runCmdOut(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return string(out)
}
