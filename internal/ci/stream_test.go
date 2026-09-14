package ci_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/runner"
	"github.com/novaforge/novaforge/internal/svcauth"
)

const streamTestSecret = "stream-test-secret"

// TestRunnerJobStreamAndArtifact is spec S-6's criterion, proven over the
// wire rather than through in-process channels: a real gRPC server on a local
// listener, the runner's own Session holding the Connect stream, the pump
// handing it a job, the job's output flowing up the stream as LogChunks into
// Redis, and a person's GetJobLogs reading those lines while the job is still
// running. Then the job ends, its log is sealed into object storage, and its
// artifact comes back byte for byte through the download stream.
//
// Every piece here was separately tested before and none of them had ever been
// joined: the dispatch tests used channels, the live tail was read with no job
// running, the pod executor held every line back until the job had ended, no
// log was ever sealed, listing a job's artifacts ignored the job id, and an
// artifact's content had no way out.
func TestRunnerJobStreamAndArtifact(t *testing.T) {
	t.Setenv("NOVAFORGE_ALLOW_LOCAL_EXEC", "1")
	pool := ciPool(t)
	rdb := ciRedis(t)
	blobs := serviceBlobstore(t)

	svc := ci.NewService(pool, rdb, blobs, &stubGitClient{}, streamTestSecret, "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(svcauth.UnaryServerInterceptor(nil, streamTestSecret)),
		grpc.ChainStreamInterceptor(svcauth.StreamServerInterceptor(nil, streamTestSecret)),
	)
	civ1.RegisterRunnerServiceServer(srv, svc.Server)
	civ1.RegisterCIServiceServer(srv, svc.Query)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	go svc.Pump.Run(ctx)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	runnerClient := civ1.NewRunnerServiceClient(conn)
	query := civ1.NewCIServiceClient(conn)

	orgID := uuid.New()
	t.Cleanup(func() { cleanupOrgRuns(t, pool, orgID) })

	reg, err := runnerClient.Register(ctx, &civ1.RegisterRequest{
		OrgId: orgID.String(), Name: "stream-test", Labels: []string{"linux"},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	session := &runner.Session{
		Client:   runnerClient,
		RunnerID: reg.GetRunnerId(),
		Executor: &runner.LocalExecutor{
			Workdir:     t.TempDir(),
			OnArtifacts: runner.UploadArtifactsThrough(runnerClient, reg.GetRunnerId()),
		},
		Heartbeat: time.Second,
	}
	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		_ = session.Run(ctx)
	}()

	// The job prints a line, then waits for the test to let it finish. The
	// release file is the only way it ends, so any read before the release is
	// a read of a job that is provably still running.
	release := filepath.Join(t.TempDir(), "release")
	run, _, err := svc.Store.CreateRun(scopedCtx(orgID), ci.Run{
		OrgID: orgID, RepoID: uuid.New(), RepoName: "widgets",
		CommitSHA: uuid.NewString(), Ref: "refs/heads/main", Status: "running",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	job, err := svc.Store.CreateJob(scopedCtx(orgID), ci.WorkflowJob{
		RunID: run.ID, Name: "build",
		RunCmd: "echo compiling widgets; " +
			"while [ ! -f '" + release + "' ]; do sleep 0.1; done; " +
			"echo widgets built; mkdir -p out; printf 'coverage: 81%%' > out/report.txt",
		ArtifactPaths: []string{"out/report.txt"},
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	tok, err := svcauth.Mint(streamTestSecret, "stream-test-reader", orgID, time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	asOrg := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)

	// 1. The first line is readable while the job runs.
	var live []string
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := query.GetJobLogs(asOrg, &civ1.GetJobLogsRequest{JobId: job.ID.String()})
		if err != nil {
			t.Fatalf("GetJobLogs while running: %v", err)
		}
		live = resp.GetLines()
		if len(live) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(live) == 0 || live[0] != "compiling widgets" {
		t.Fatalf("no live log line reached GetJobLogs while the job ran: %q", live)
	}
	running, err := svc.Store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if running.Status != "running" {
		t.Fatalf("the log was read after the job stopped running (status %q), so it proves nothing about a live log", running.Status)
	}
	for _, l := range live {
		if l == "widgets built" {
			t.Fatal("the job printed its last line before it was released")
		}
	}

	// 2. Let it finish and wait for the runner's report to settle the job.
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var finished ci.WorkflowJob
	for time.Now().Before(deadline) {
		finished, err = svc.Store.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if finished.Status != "pending" && finished.Status != "running" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if finished.Status != "success" {
		t.Fatalf("job settled %q (%s), want success", finished.Status, finished.Detail)
	}

	// 3. The finished log is sealed into object storage, and reads back whole.
	sealed, err := blobs.Get(ctx, "logs/"+job.ID.String()+".txt")
	if err != nil {
		t.Fatalf("the finished job's log was never sealed into object storage: %v", err)
	}
	sealedBody, _ := io.ReadAll(sealed)
	sealed.Close()
	if !strings.Contains(string(sealedBody), "widgets built") {
		t.Fatalf("sealed log is missing the job's last line: %q", sealedBody)
	}
	after, err := query.GetJobLogs(asOrg, &civ1.GetJobLogsRequest{JobId: job.ID.String()})
	if err != nil {
		t.Fatalf("GetJobLogs after the job: %v", err)
	}
	if got := strings.Join(after.GetLines(), "\n"); got != "compiling widgets\nwidgets built" {
		t.Fatalf("log after the job = %q, want both lines once and no artifact payload", got)
	}

	// 4. The artifact is listed for the job, and its content downloads intact.
	arts, err := query.ListArtifacts(asOrg, &civ1.ListArtifactsRequest{JobId: job.ID.String()})
	if err != nil {
		t.Fatalf("ListArtifacts by job: %v", err)
	}
	if len(arts.GetArtifacts()) != 1 || arts.GetArtifacts()[0].GetName() != "report.txt" {
		t.Fatalf("job artifacts = %+v, want report.txt", arts.GetArtifacts())
	}
	content, name := downloadArtifact(t, asOrg, query, arts.GetArtifacts()[0].GetId())
	if name != "report.txt" || string(content) != "coverage: 81%" {
		t.Fatalf("downloaded %q = %q, want report.txt = %q", name, content, "coverage: 81%")
	}

	// 5. Another organization cannot read the log or the artifact.
	otherTok, err := svcauth.Mint(streamTestSecret, "stream-test-reader", uuid.New(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asOther := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+otherTok)
	if _, err := query.GetJobLogs(asOther, &civ1.GetJobLogsRequest{JobId: job.ID.String()}); status.Code(err) != codes.NotFound {
		t.Fatalf("another organization read the job log: %v", err)
	}
	stream, err := query.DownloadArtifact(asOther, &civ1.DownloadArtifactRequest{ArtifactId: arts.GetArtifacts()[0].GetId()})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("another organization downloaded the artifact: %v", err)
	}

	cancel()
	<-sessionDone
}

func downloadArtifact(t *testing.T, ctx context.Context, query civ1.CIServiceClient, id string) ([]byte, string) {
	t.Helper()
	stream, err := query.DownloadArtifact(ctx, &civ1.DownloadArtifactRequest{ArtifactId: id})
	if err != nil {
		t.Fatalf("DownloadArtifact: %v", err)
	}
	var buf bytes.Buffer
	var name string
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("DownloadArtifact recv: %v", err)
		}
		if name == "" {
			name = msg.GetName()
		}
		buf.Write(msg.GetData())
	}
	return buf.Bytes(), name
}
