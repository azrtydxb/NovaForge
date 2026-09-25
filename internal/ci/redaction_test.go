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
	for _, tc := range []struct{ name, binding, value, leaked string }{
		{"scalar", "DEPLOY_TOKEN", "scalar-sensitive-token", "scalar-sensitive-token"},
		{"json", "JSON_LOGIN", `{"password":"sensitive-password","username":"sensitive-user"}`, "sensitive-password"},
		{"json-unicode-metadata", "JSON_LOGIN", `{"note":"\ud83d\ude00","password":"sensitive-password"}`, "sensitive-password"},
		{"json-unicode-short", "JSON_LOGIN", `{"note":"\ud83d\ude00","password":"pw"}`, "pw"},
		{"json-unicode-number", "JSON_LOGIN", `{"note":"\ud83d\ude00","password":9007199254740993}`, "9007199254740993"},
		{"json-multiline", "JSON_LOGIN", `{"password":"first-sensitive-line\nsecond-sensitive-line","username":"sensitive-user"}`, "second-sensitive-line"},
		{"kubeconfig", "DEPLOY_TOKEN", "users:\n- name: ci-user\n  user:\n    token: kube-sensitive-token\n", "kube-sensitive-token"},
	} {
		t.Run(tc.name, func(t *testing.T) { testJobCredentialRedaction(t, tc.binding, tc.value, tc.leaked) })
	}
}

func testJobCredentialRedaction(t *testing.T, binding, value, leaked string) {
	t.Helper()
	s := newCredentialStack(t)
	if err := s.putDynamicSecret(binding, "staging", value); err != nil {
		t.Fatal(err)
	}
	jobID := s.job("refs/heads/main", "prints-its-token", "staging", binding)

	rdb := ciRedis(t)
	// This fixture intentionally has no sealed object store. Remove only its
	// randomly identified live stream; never flush the shared Redis database.
	t.Cleanup(func() {
		if err := rdb.Del(context.Background(), "joblog:"+jobID.String()).Err(); err != nil {
			t.Error(err)
		}
	})
	logs := ci.NewLogSink(rdb, nil)
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
	if got[jobID.String()].GetSecretEnv()[binding] != value {
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
	line := "deploying with token " + leaked + " now"
	for _, msg := range []*civ1.ConnectRequest{
		{RunnerId: runner, Token: s.runnerToken, Payload: &civ1.ConnectRequest_Heartbeat{Heartbeat: &civ1.Heartbeat{}}},
		{RunnerId: runner, Token: s.runnerToken, Payload: &civ1.ConnectRequest_LogChunk{LogChunk: &civ1.LogChunk{JobId: jobID.String(), Line: line}}},
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
	if strings.Contains(stored[0], leaked) || !strings.Contains(stored[0], "***") {
		t.Errorf("stored log line carries the credential or was not masked")
	}

	// A terminal status's detail is stored too, and is masked the same way.
	if _, err := client.ReportStatus(context.Background(), &civ1.ReportStatusRequest{
		RunnerId: runner, Token: s.runnerToken, JobId: jobID.String(), Status: "failure", Detail: "exit 1: bad token " + leaked,
	}); err != nil {
		t.Fatal(err)
	}
	if j := s.jobState(jobID); strings.Contains(j.Detail, leaked) || !strings.Contains(j.Detail, "***") {
		t.Fatalf("job detail %q carries the credential", j.Detail)
	}
}
