package ci_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
)

// TestJobLogNeverCarriesItsCredential pins that a brokered value cannot reach
// a job's stored log even when the runner sends it: the job prints its token,
// the runner forwards the line unredacted over the real RunnerService stream,
// and what is stored — what every member of the organization can read — has
// the value masked. The runner redacts too, but a runner is the part of CI an
// organization runs itself, so the platform does not rely on it.
func TestJobLogNeverCarriesItsCredential(t *testing.T) {
	s := newCredentialStack(t)
	value := secretValue("logged")
	if err := s.broker.PutValue(context.Background(), s.org, "DEPLOY_TOKEN", "staging", value); err != nil {
		t.Fatal(err)
	}
	jobID := s.job("refs/heads/main", "prints-its-token", "staging", "DEPLOY_TOKEN")

	logs := ci.NewLogSink(ciRedis(t), nil)
	srv := ci.NewServer(s.store, s.dispatcher)
	srv.SetLogSink(logs)
	srv.SetRedactions(s.redactions)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	civ1.RegisterRunnerServiceServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	s.start()
	got := s.waitDispatched(20*time.Second, 1)
	if got[jobID.String()].GetSecretEnv()["DEPLOY_TOKEN"] != value {
		t.Fatalf("job was not dispatched with its credential: %v", got)
	}

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := civ1.NewRunnerServiceClient(conn)
	stream, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := s.runnerID.String()
	line := "deploying with token " + value + " now"
	for _, msg := range []*civ1.ConnectRequest{
		{RunnerId: runner, Payload: &civ1.ConnectRequest_Heartbeat{Heartbeat: &civ1.Heartbeat{}}},
		{RunnerId: runner, Payload: &civ1.ConnectRequest_LogChunk{LogChunk: &civ1.LogChunk{JobId: jobID.String(), Line: line}}},
	} {
		if err := stream.Send(msg); err != nil {
			t.Fatalf("send: %v", err)
		}
	}

	var stored []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(stored) == 0 {
		stored, err = logs.Snapshot(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(stored) != 1 {
		t.Fatalf("stored log = %q, want the one line", stored)
	}
	if strings.Contains(stored[0], value) || !strings.Contains(stored[0], "***") {
		t.Fatalf("stored log line %q carries the credential or was not masked", stored[0])
	}

	// A terminal status's detail is stored too, and is masked the same way.
	if _, err := client.ReportStatus(context.Background(), &civ1.ReportStatusRequest{
		RunnerId: runner, JobId: jobID.String(), Status: "failure", Detail: "exit 1: bad token " + value,
	}); err != nil {
		t.Fatal(err)
	}
	if j := s.jobState(jobID); strings.Contains(j.Detail, value) {
		t.Fatalf("job detail %q carries the credential", j.Detail)
	}
}
