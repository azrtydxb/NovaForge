package retention_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/retention"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

// retentionPool returns a pool with both the retention and ci schemas
// migrated: Sweep spans both, since it enforces each organization's
// LogDays against the raw CI data ci owns. Tests never truncate these
// shared, exclusively-owned-by-this-package tables — the database is a real
// external instance other test runs may be using concurrently — and instead
// scope every assertion to the specific org, job, and object keys each test
// creates for itself, so leftover or concurrently-written rows from
// elsewhere never affect the outcome.
func retentionPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "retention", os.DirFS("migrations")); err != nil {
		t.Fatalf("Migrate retention: %v", err)
	}
	if err := database.Migrate(url, "ci", os.DirFS("../ci/migrations")); err != nil {
		t.Fatalf("Migrate ci: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func retentionBlobstore(t *testing.T) *blobstore.Client {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	c, err := blobstore.New(context.Background(), blobstore.Options{
		Endpoint:  ep,
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		Bucket:    "novaforge-test-retention-" + uuid.NewString()[:8],
		UseSSL:    false,
	})
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	return c
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}

// seedFinishedJob creates a run and a job on it whose finished_at is
// backdated to age days ago, returning the job id.
func seedFinishedJob(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, age time.Duration) uuid.UUID {
	t.Helper()
	store := ci.NewStore(pool)
	ctx := scopedCtx(orgID)
	run, created, err := store.CreateRun(ctx, ci.Run{OrgID: orgID, RepoID: uuid.New(), CommitSHA: uuid.NewString(), Ref: "refs/heads/main"})
	if err != nil || !created {
		t.Fatalf("CreateRun: run=%+v created=%v err=%v", run, created, err)
	}
	t.Cleanup(func() {
		store.DeleteRun(context.Background(), run.ID)
	})
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

func TestDefaultPolicyWhenUnset(t *testing.T) {
	pool := retentionPool(t)
	store := retention.NewStore(pool)

	policy, err := store.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if policy.LogDays != 90 {
		t.Fatalf("want default LogDays 90, got %d", policy.LogDays)
	}
	if policy.EvidenceDays != 0 {
		t.Fatalf("want default EvidenceDays 0, got %d", policy.EvidenceDays)
	}
}

func TestSweepDeletesExpiredLogsOnly(t *testing.T) {
	pool := retentionPool(t)
	blobs := retentionBlobstore(t)
	sweeper := retention.NewSweeper(pool, blobs)

	orgID := uuid.New()
	oldJobID := seedFinishedJob(t, pool, orgID, 100*24*time.Hour)
	recentJobID := seedFinishedJob(t, pool, orgID, 10*24*time.Hour)

	ctx := context.Background()
	oldKey := "logs/" + oldJobID.String() + ".txt"
	recentKey := "logs/" + recentJobID.String() + ".txt"
	if err := blobs.Put(ctx, oldKey, strings.NewReader("old log"), 7, "text/plain"); err != nil {
		t.Fatalf("seed old log: %v", err)
	}
	if err := blobs.Put(ctx, recentKey, strings.NewReader("recent log"), 10, "text/plain"); err != nil {
		t.Fatalf("seed recent log: %v", err)
	}

	if _, err := sweeper.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if _, err := blobs.Get(ctx, oldKey); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("want old log deleted (ErrNotFound), got err=%v", err)
	}
	if r, err := blobs.Get(ctx, recentKey); err != nil {
		t.Fatalf("want recent log to remain, got err=%v", err)
	} else {
		r.Close()
	}
}

func TestSweepHonoursZeroAsForever(t *testing.T) {
	pool := retentionPool(t)
	blobs := retentionBlobstore(t)
	store := retention.NewStore(pool)
	sweeper := retention.NewSweeper(pool, blobs)

	orgID := uuid.New()
	if err := store.Set(context.Background(), retention.Policy{OrgID: orgID, LogDays: 0, EvidenceDays: 0}); err != nil {
		t.Fatalf("Set policy: %v", err)
	}

	jobID := seedFinishedJob(t, pool, orgID, 365*24*time.Hour)
	ctx := context.Background()
	key := "logs/" + jobID.String() + ".txt"
	if err := blobs.Put(ctx, key, strings.NewReader("ancient log"), 11, "text/plain"); err != nil {
		t.Fatalf("seed ancient log: %v", err)
	}

	if _, err := sweeper.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if r, err := blobs.Get(ctx, key); err != nil {
		t.Fatalf("want log to remain forever under LogDays=0, got err=%v", err)
	} else {
		r.Close()
	}
}
