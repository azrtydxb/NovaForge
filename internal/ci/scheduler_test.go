package ci_test

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/events"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func redisURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_REDIS_URL")
	if u == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	return u
}

// ciPool returns a connected, migrated pool onto the ci schema. Every test
// that uses it scopes its own assertions by a freshly generated org and repo
// id (ListRuns, List) or by comparing specific row ids it created itself, so
// leftover rows from other tests or other concurrent test runs against this
// shared external database never affect it — except ClaimJob, which claims
// globally across every pending job regardless of org; see ciPoolExclusive.
func ciPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "ci", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ciPoolExclusive gives platform-wide worker tests their own real database.
// Truncating the shared schema used to erase other packages' evidence during
// go test ./...; test exclusivity must never mean deleting another test's data.
func ciPoolExclusive(t *testing.T) *pgxpool.Pool {
	t.Helper()
	owner := ciPool(t)
	u, err := url.Parse(dbURL(t))
	if err != nil {
		t.Fatal(err)
	}
	name := "novaforge_ci_test_" + uuid.New().String()
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := owner.Exec(context.Background(), "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("create isolated CI test database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), "DROP DATABASE "+quoted); err != nil {
			t.Errorf("remove isolated CI test database: %v", err)
		}
	})
	u.Path, u.RawPath = "/"+name, ""
	if err := database.Migrate(u.String(), "ci", ci.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func ciRedis(t *testing.T) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(redisURL(t))
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}

// stubGitClient serves a fixed workflow document (or a NotFound error when
// content is empty) for GetBlob, and panics on any other method.
type stubGitClient struct {
	gitv1.GitServiceClient
	content []byte
	missing bool
}

func (s *stubGitClient) GetBlob(ctx context.Context, in *gitv1.GetBlobRequest, opts ...grpc.CallOption) (*gitv1.GetBlobResponse, error) {
	if s.missing {
		return nil, status.Error(codes.NotFound, "blob not found")
	}
	return &gitv1.GetBlobResponse{Content: s.content}, nil
}

const oneJobWorkflow = "jobs:\n  test:\n    run: go test ./...\n"

const twoJobWorkflow = "jobs:\n  build:\n    run: go build ./...\n  test:\n    run: go test ./...\n    needs: [build]\n"

func newTestScheduler(t *testing.T, git gitv1.GitServiceClient) (*ci.Scheduler, *ci.Store, *redis.Client) {
	t.Helper()
	pool := ciPool(t)
	store := ci.NewStore(pool)
	rdb := ciRedis(t)
	stream := "stream:test:git:push:" + uuid.NewString()
	t.Cleanup(func() { rdb.Del(context.Background(), stream) })
	sched := ci.NewScheduler(rdb, store, git, ci.SchedulerConfig{
		HMACSecret: "test-secret",
		Stream:     stream,
		Group:      "ci-engine-test",
	})
	return sched, store, rdb
}

// cleanupOrgRuns deletes every ci.workflow_runs row for orgID (cascading to
// its jobs and artifacts) once the test finishes, so repeated runs of this
// suite against the shared external test database don't leave permanent
// rows behind that a later, unrelated test's global ClaimJob could pick up.
func cleanupOrgRuns(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM ci.workflow_runs WHERE org_id = $1", orgID)
	})
}

func publishPush(t *testing.T, rdb *redis.Client, stream string, evt events.PushEvent) {
	t.Helper()
	if err := events.Publish(context.Background(), rdb, stream, evt); err != nil {
		t.Fatalf("publish push event: %v", err)
	}
}

// schedulerStream extracts the stream name a Scheduler was configured with,
// by re-deriving it the same way newTestScheduler built it — tests keep the
// stream name themselves rather than reaching into the Scheduler.
func TestPushSchedulesRun(t *testing.T) {
	git := &stubGitClient{content: []byte(oneJobWorkflow)}
	pool := ciPool(t)
	store := ci.NewStore(pool)
	rdb := ciRedis(t)
	stream := "stream:test:git:push:" + uuid.NewString()
	t.Cleanup(func() { rdb.Del(context.Background(), stream) })
	sched := ci.NewScheduler(rdb, store, git, ci.SchedulerConfig{
		Stream: stream, Group: "ci-engine-test-" + uuid.NewString(), HMACSecret: "test-secret"})

	orgID := uuid.New()
	repoID := uuid.New()
	cleanupOrgRuns(t, pool, orgID)
	evt := events.PushEvent{
		OrgID: orgID, RepoID: repoID, PusherID: uuid.New(),
		Ref: "refs/heads/main", OldSHA: "old", NewSHA: uuid.NewString(),
		At: time.Now().UTC(),
	}
	publishPush(t, rdb, stream, evt)

	if err := sched.ProcessAvailable(context.Background()); err != nil {
		t.Fatalf("ProcessAvailable: %v", err)
	}

	ctx := scopedCtx(orgID)
	runs, err := store.ListRuns(ctx, orgID, repoID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(runs))
	}
	if runs[0].CommitSHA != evt.NewSHA {
		t.Fatalf("want commit sha %q, got %q", evt.NewSHA, runs[0].CommitSHA)
	}
}

func TestDuplicatePushIsIdempotent(t *testing.T) {
	git := &stubGitClient{content: []byte(oneJobWorkflow)}
	pool := ciPool(t)
	store := ci.NewStore(pool)
	rdb := ciRedis(t)
	stream := "stream:test:git:push:" + uuid.NewString()
	t.Cleanup(func() { rdb.Del(context.Background(), stream) })
	sched := ci.NewScheduler(rdb, store, git, ci.SchedulerConfig{
		Stream: stream, Group: "ci-engine-test-" + uuid.NewString(), HMACSecret: "test-secret"})

	orgID := uuid.New()
	repoID := uuid.New()
	cleanupOrgRuns(t, pool, orgID)
	evt := events.PushEvent{
		OrgID: orgID, RepoID: repoID, PusherID: uuid.New(),
		Ref: "refs/heads/main", OldSHA: "old", NewSHA: uuid.NewString(),
		At: time.Now().UTC(),
	}
	publishPush(t, rdb, stream, evt)
	publishPush(t, rdb, stream, evt)

	if err := sched.ProcessAvailable(context.Background()); err != nil {
		t.Fatalf("ProcessAvailable first: %v", err)
	}
	if err := sched.ProcessAvailable(context.Background()); err != nil {
		t.Fatalf("ProcessAvailable second: %v", err)
	}

	ctx := scopedCtx(orgID)
	runs, err := store.ListRuns(ctx, orgID, repoID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want exactly 1 run after duplicate delivery, got %d", len(runs))
	}
}

func TestMissingWorkflowSkipsSilently(t *testing.T) {
	git := &stubGitClient{missing: true}
	pool := ciPool(t)
	store := ci.NewStore(pool)
	rdb := ciRedis(t)
	stream := "stream:test:git:push:" + uuid.NewString()
	t.Cleanup(func() { rdb.Del(context.Background(), stream) })
	sched := ci.NewScheduler(rdb, store, git, ci.SchedulerConfig{
		Stream: stream, Group: "ci-engine-test-" + uuid.NewString(), HMACSecret: "test-secret"})

	orgID := uuid.New()
	repoID := uuid.New()
	evt := events.PushEvent{
		OrgID: orgID, RepoID: repoID, PusherID: uuid.New(),
		Ref: "refs/heads/main", OldSHA: "old", NewSHA: uuid.NewString(),
		At: time.Now().UTC(),
	}
	publishPush(t, rdb, stream, evt)

	if err := sched.ProcessAvailable(context.Background()); err != nil {
		t.Fatalf("ProcessAvailable: %v", err)
	}

	ctx := scopedCtx(orgID)
	runs, err := store.ListRuns(ctx, orgID, repoID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("want 0 runs for repository with no workflow, got %d", len(runs))
	}
}

func TestJobBlockedUntilNeedsSucceed(t *testing.T) {
	pool := ciPoolExclusive(t)
	store := ci.NewStore(pool)

	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)
	run, created, err := store.CreateRun(ctx, ci.Run{OrgID: orgID, RepoID: repoID, CommitSHA: uuid.NewString(), Ref: "refs/heads/main"})
	if err != nil || !created {
		t.Fatalf("CreateRun: run=%+v created=%v err=%v", run, created, err)
	}

	if _, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "build", RunCmd: "go build ./..."}); err != nil {
		t.Fatalf("CreateJob build: %v", err)
	}
	if _, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "test", RunCmd: "go test ./...", Needs: []string{"build"}}); err != nil {
		t.Fatalf("CreateJob test: %v", err)
	}

	runnerID := uuid.New()
	job, err := store.ClaimJob(ctx, runnerID, nil)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if job.Name != "build" {
		t.Fatalf("want to claim 'build' first (its needs are satisfied), got %q", job.Name)
	}

	// "test" still needs "build" to succeed — no claimable job right now.
	if _, err := store.ClaimJob(ctx, uuid.New(), nil); err == nil {
		t.Fatal("want ClaimJob to find no claimable job while 'test' is blocked on 'build'")
	}

	if err := store.SetJobStatus(ctx, job.ID, "success", ""); err != nil {
		t.Fatalf("SetJobStatus: %v", err)
	}

	job2, err := store.ClaimJob(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatalf("ClaimJob after build succeeded: %v", err)
	}
	if job2.Name != "test" {
		t.Fatalf("want to claim 'test' once 'build' succeeded, got %q", job2.Name)
	}
}
