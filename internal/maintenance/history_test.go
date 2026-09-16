package maintenance_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// historyCI stores actual go test -json output in CI's real log stores and
// exposes it through the same authenticated QueryServer used in production.
// Repeated tests within a single job also expose flakiness: -count=2 runs
// the identical code twice without pretending a job exit code names a test.
func historyCI(t *testing.T, orgID, repoID uuid.UUID) civ1.CIServiceClient {
	t.Helper()
	ctx := context.Background()
	url := proposeDBURL(t)
	if err := database.Migrate(url, "ci", ci.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	opt, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })
	blobs, err := blobstore.New(ctx, blobstore.Options{
		Endpoint: os.Getenv("TEST_S3_ENDPOINT"), AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), Bucket: "novaforge-test-maintenance-history",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := ci.NewStore(pool)
	logs := ci.NewLogSink(rdb, blobs)
	run, _, err := store.CreateRun(ctx, ci.Run{OrgID: orgID, RepoID: repoID, CommitSHA: strings.Repeat("a", 40), Ref: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteRun(context.Background(), run.ID) })
	job, err := store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "tests", RunCmd: "go test -json -count=2", Status: "failure"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Delete(context.Background(), job.ID) })
	dir := t.TempDir()
	file := filepath.Join(dir, "flaky_test.go")
	if err := os.WriteFile(file, []byte("package probe\nimport \"testing\"\nvar calls int\nfunc TestFlaky(t *testing.T) { calls++; if calls == 2 { t.Fatal(\"second invocation fails\") } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("go", "test", "-json", "-count=2", file).CombinedOutput()
	if err == nil || !strings.Contains(string(out), `"Action":"pass"`) || !strings.Contains(string(out), `"Action":"fail"`) {
		t.Fatalf("fixture must actually pass and fail: %v: %s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if err := logs.Append(ctx, job.ID, line); err != nil {
			t.Fatal(err)
		}
	}
	// Seal exercises object storage as well as Redis, just like a finished job.
	if _, err := logs.Seal(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, sweepSecret)))
	civ1.RegisterCIServiceServer(srv, ci.NewQueryServer(store, logs, nil, blobs))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return civ1.NewCIServiceClient(conn)
}
