package gitops_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gitops"
)

const zeroSHA = "0000000000000000000000000000000000000000"

// pushStreamWatcher reads the real, shared push stream from the position it
// had when the watcher was made, so an event left by an earlier run — or by
// another test using the same Redis — can never be mistaken for this one.
type pushStreamWatcher struct {
	rdb  *redis.Client
	from string
}

func newPushStreamWatcher(t *testing.T) *pushStreamWatcher {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	from := "0"
	last, err := rdb.XRevRangeN(context.Background(), events.StreamGitPush, "+", "-", 1).Result()
	if err != nil && err != redis.Nil {
		t.Fatalf("read stream tail: %v", err)
	}
	if len(last) == 1 {
		from = last[0].ID
	}
	return &pushStreamWatcher{rdb: rdb, from: from}
}

// await returns the push event carrying newSHA on ref, failing the test if
// none arrives.
func (w *pushStreamWatcher) await(t *testing.T, ref, newSHA string) events.PushEvent {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := w.rdb.XRead(context.Background(), &redis.XReadArgs{
			Streams: []string{events.StreamGitPush, w.from},
			Count:   100,
			Block:   time.Second,
		}).Result()
		if err != nil && err != redis.Nil {
			t.Fatalf("XRead: %v", err)
		}
		for _, st := range res {
			for _, m := range st.Messages {
				w.from = m.ID
				raw, _ := m.Values["data"].(string)
				var evt events.PushEvent
				if json.Unmarshal([]byte(raw), &evt) != nil {
					continue
				}
				if evt.Ref == ref && evt.NewSHA == newSHA {
					return evt
				}
			}
		}
	}
	t.Fatalf("no push event for %s at %s arrived within 15s", ref, newSHA)
	return events.PushEvent{}
}

// wireRealPublisher publishes through the same Redis publisher git-platform
// installs at startup, resolving repository ids through the server itself.
func wireRealPublisher(t *testing.T, w *pushStreamWatcher, srv *gitops.Server) {
	t.Helper()
	gitops.WireRedisPushEventsWithRepoID(w.rdb, func(ctx context.Context, orgID uuid.UUID, repo string) (uuid.UUID, error) {
		resp, err := srv.GetRepo(authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"}), &gitv1.GetRepoRequest{Name: repo})
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.MustParse(resp.GetRepo().GetId()), nil
	})
	t.Cleanup(func() { gitops.PushPublisher = nil })
}

func headOf(t *testing.T, srv *gitops.Server, ctx context.Context, repo, branch string) string {
	t.Helper()
	list, err := srv.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repo, Ref: branch, Limit: 1})
	if err != nil || len(list.GetCommits()) == 0 {
		t.Fatalf("head of %s: %v", branch, err)
	}
	return list.GetCommits()[0].GetSha()
}

// TestMergeRPCPublishesPushEvent pins the seam that left the code index and
// CI blind to every merge made through the platform: a merge moved the
// target branch in the bare repository with no push event, so the indexer
// kept describing the branch as it was before the merge and CI never ran on
// the merged result. A merge must look to every consumer exactly like a push
// of the target ref from its old head to the merge commit.
func TestMergeRPCPublishesPushEvent(t *testing.T) {
	watcher := newPushStreamWatcher(t)
	srv, root := newGitGRPCServer(t)
	wireRealPublisher(t, watcher, srv)

	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	name := "merge-evt-" + uuid.NewString()[:8]
	created, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	repoPath := filepath.Join(root, orgID.String(), name+".git")
	seedInitialCommit(t, repoPath)

	work := t.TempDir()
	runGit(t, "", "clone", repoPath, work)
	runGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "feature.txt")
	runGit(t, work, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-m", "add feature")
	runGit(t, work, "push", "origin", "feature")

	before := headOf(t, srv, ctx, name, "main")
	resp, err := srv.Merge(ctx, &gitv1.MergeRequest{Repo: name, SourceRef: "feature", TargetRef: "main", Method: "merge"})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	evt := watcher.await(t, "refs/heads/main", resp.GetMergeSha())
	if evt.OldSHA != before {
		t.Fatalf("merge event old sha = %q, want main's head before the merge %q", evt.OldSHA, before)
	}
	if evt.RepoID.String() != created.GetRepo().GetId() || evt.OrgID != orgID || evt.RepoName != name {
		t.Fatalf("merge event names org %s repo %s (%s), want %s %s (%s)", evt.OrgID, evt.RepoID, evt.RepoName, orgID, created.GetRepo().GetId(), name)
	}
	scope, _ := authz.FromContext(ctx)
	if evt.PusherID != scope.ActorID || evt.PusherKind != "user" {
		t.Fatalf("merge event pusher = %s/%s, want the caller %s/user", evt.PusherID, evt.PusherKind, scope.ActorID)
	}
}

// TestCreateCommitAndBranchRPCsPublishPushEvents covers the other two ways the
// platform writes a ref without a git push: git.commit, which is how every
// agent's work reaches a repository, and CreateBranch.
func TestCreateCommitAndBranchRPCsPublishPushEvents(t *testing.T) {
	watcher := newPushStreamWatcher(t)
	srv, root := newGitGRPCServer(t)
	wireRealPublisher(t, watcher, srv)

	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	name := "commit-evt-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	seedInitialCommit(t, filepath.Join(root, orgID.String(), name+".git"))

	before := headOf(t, srv, ctx, name, "main")
	commit, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{
		Repo: name, Branch: "main", Message: "agent change",
		Files: []*gitv1.FileChange{{Path: "a.txt", Content: []byte("a\n")}},
	})
	if err != nil {
		t.Fatalf("CreateCommit: %v", err)
	}
	evt := watcher.await(t, "refs/heads/main", commit.GetSha())
	if evt.OldSHA != before {
		t.Fatalf("commit event old sha = %q, want %q", evt.OldSHA, before)
	}

	// A commit that creates its branch reports the zero sha as the old value,
	// exactly as git does for a pushed new branch.
	fresh, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{
		Repo: name, Branch: "agents/NF-1/work", Message: "first on branch",
		Files: []*gitv1.FileChange{{Path: "b.txt", Content: []byte("b\n")}},
	})
	if err != nil {
		t.Fatalf("CreateCommit on a new branch: %v", err)
	}
	evt = watcher.await(t, "refs/heads/agents/NF-1/work", fresh.GetSha())
	if strings.Trim(evt.OldSHA, "0") != "" {
		t.Fatalf("new-branch commit event old sha = %q, want the zero sha", evt.OldSHA)
	}

	branch, err := srv.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: name, Name: "feature/y"})
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	evt = watcher.await(t, "refs/heads/feature/y", branch.GetRef().GetSha())
	if evt.OldSHA != zeroSHA {
		t.Fatalf("branch event old sha = %q, want %q", evt.OldSHA, zeroSHA)
	}
}
