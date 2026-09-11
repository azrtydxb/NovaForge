package ci_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
)

// seedFinishedJobForSweep creates a run and a finished job backdated by age,
// giving the retention sweeper a real row to consider.
func seedFinishedJobForSweep(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, age time.Duration) uuid.UUID {
	t.Helper()
	store := ci.NewStore(pool)
	ctx := scopedCtx(orgID)
	run, created, err := store.CreateRun(ctx, ci.Run{OrgID: orgID, RepoID: uuid.New(), CommitSHA: uuid.NewString(), Ref: "refs/heads/main"})
	if err != nil || !created {
		t.Fatalf("CreateRun: run=%+v created=%v err=%v", run, created, err)
	}
	job, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "build", RunCmd: "true"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	finishedAt := time.Now().Add(-age)
	if _, err := pool.Exec(context.Background(),
		`UPDATE ci.workflow_jobs SET status = 'success', finished_at = $1 WHERE id = $2`,
		finishedAt, job.ID,
	); err != nil {
		t.Fatalf("backdate finished_at: %v", err)
	}
	return job.ID
}

func serviceBlobstore(t *testing.T) *blobstore.Client {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	c, err := blobstore.New(context.Background(), blobstore.Options{
		Endpoint:  ep,
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		Bucket:    "novaforge-test-ci-service-" + uuid.NewString()[:8],
		UseSSL:    false,
	})
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	return c
}

// TestNewServiceWiresEveryComponent asserts NewService returns a fully
// wired Service — a nil Store, Server, Scheduler, or Sweeper here would
// mean cmd/ci-runner's main starts a gRPC server, a scheduler, or a
// retention sweeper that silently does nothing.
func TestNewServiceWiresEveryComponent(t *testing.T) {
	pool := ciPool(t)
	if err := database.Migrate(dbURL(t), "retention", os.DirFS("../retention/migrations")); err != nil {
		t.Fatalf("migrate retention schema: %v", err)
	}
	rdb := ciRedis(t)
	blobs := serviceBlobstore(t)
	git := &stubGitClient{content: []byte(oneJobWorkflow)}

	svc := ci.NewService(pool, rdb, blobs, git, "test-secret")
	if svc.Store == nil || svc.Dispatcher == nil || svc.Logs == nil || svc.Artifacts == nil {
		t.Fatalf("NewService left a storage component nil: %+v", svc)
	}
	if svc.Server == nil {
		t.Fatal("NewService left Server nil")
	}
	if svc.Scheduler == nil {
		t.Fatal("NewService left Scheduler nil")
	}
	if svc.Sweeper == nil {
		t.Fatal("NewService left Sweeper nil")
	}
}

// TestRunSweepsOnATightTicker asserts Service.Run actually invokes the
// retention sweeper on its configured interval, rather than merely holding
// a reference to it that nothing ever calls — with SweepEvery set to a few
// milliseconds so the test does not wait an hour, and a finished job with an
// expired sealed log so a real sweep pass has something to delete.
func TestRunSweepsOnATightTicker(t *testing.T) {
	pool := ciPool(t)
	if err := database.Migrate(dbURL(t), "retention", os.DirFS("../retention/migrations")); err != nil {
		t.Fatalf("migrate retention schema: %v", err)
	}
	rdb := ciRedis(t)
	blobs := serviceBlobstore(t)
	git := &stubGitClient{content: []byte(oneJobWorkflow)}

	orgID := uuid.New()
	cleanupOrgRuns(t, pool, orgID)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO retention.retention_policies (org_id, log_days, evidence_days) VALUES ($1, 1, 0)`, orgID); err != nil {
		t.Fatalf("seed retention policy: %v", err)
	}

	job := seedFinishedJobForSweep(t, pool, orgID, 48*time.Hour)
	key := "logs/" + job.String() + ".txt"
	body := []byte("stale log")
	if err := blobs.Put(context.Background(), key, bytes.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("seed sealed log: %v", err)
	}

	svc := ci.NewService(pool, rdb, blobs, git, "test-secret")
	svc.SweepEvery = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Run(ctx)

	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("Run did not sweep the expired log within the deadline")
		case <-tick.C:
			if _, err := blobs.Get(context.Background(), key); err != nil {
				return // deleted — the sweeper ran.
			}
		}
	}
}
