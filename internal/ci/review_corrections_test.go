package ci_test

import (
	"context"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"net"
	"testing"
	"time"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
)

func TestPredecessorRunnerCannotFinishWithoutSequenceDeclaration(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	client, stream, connection := connectReviewRunner(t, s)

	job := s.job("refs/heads/main", "predecessor", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	if err := s.store.StartClaimedJob(ctx, job, s.runnerID); err != nil {
		t.Fatal(err)
	}
	// An incarnation-aware older runner sends status before its in-flight
	// sequence-less log. Omission must not masquerade as declared zero output.
	_, err := client.ReportStatus(ctx, &civ1.ReportStatusRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, ConnectionId: connection.String(), JobId: job.String(), Status: "success"})
	if err == nil {
		t.Fatal("missing final-sequence declaration admitted success")
	}
	if s.jobState(job).Status != "running" {
		t.Fatal("rejected receipt changed job")
	}
	if err := stream.Send(&civ1.ConnectRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, ConnectionId: connection.String(), Payload: &civ1.ConnectRequest_LogChunk{LogChunk: &civ1.LogChunk{JobId: job.String(), Line: "predecessor unsequenced output"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("unsequenced predecessor log accepted")
	}
}

func TestExplicitZeroOutputReceiptOverWire(t *testing.T) {
	s := newCredentialStack(t)
	client, _, connection := connectReviewRunner(t, s)
	ctx := context.Background()
	job := s.job("refs/heads/main", "zero-output", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	if err := s.store.StartClaimedJob(ctx, job, s.runnerID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReportStatus(ctx, &civ1.ReportStatusRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, ConnectionId: connection.String(), JobId: job.String(), Status: "success", LastLogSequence: proto.Int64(0)}); err != nil {
		t.Fatal(err)
	}
	if s.jobState(job).Status != "success" {
		t.Fatal("explicit zero-output declaration not accepted")
	}
}

func connectReviewRunner(t *testing.T, s *credentialStack) (civ1.RunnerServiceClient, civ1.RunnerService_ConnectClient, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	server := ci.NewServer(s.store, s.dispatcher)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	civ1.RegisterRunnerServiceServer(rpc, server)
	go rpc.Serve(lis)
	t.Cleanup(rpc.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := civ1.NewRunnerServiceClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&civ1.ConnectRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, Payload: &civ1.ConnectRequest_Heartbeat{Heartbeat: &civ1.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	hello, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	id, err := uuid.Parse(hello.GetConnectionId())
	if err != nil {
		t.Fatal(err)
	}
	return client, stream, id
}

func TestRunnerInterruptionSettlesRun(t *testing.T) {
	for _, mode := range []string{"replacement", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			s := newCredentialStack(t)
			ctx := context.Background()
			connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
			if err != nil {
				t.Fatal(err)
			}
			job := s.job("refs/heads/main", mode, "staging")
			if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
				t.Fatal(err)
			}
			if err := s.store.StartClaimedJob(ctx, job, s.runnerID); err != nil {
				t.Fatal(err)
			}
			runID := s.jobState(job).RunID
			if mode == "replacement" {
				_, err = s.store.BeginRunnerConnection(ctx, s.runnerID)
			} else {
				err = s.store.EndRunnerConnection(ctx, s.runnerID, connection)
			}
			if err != nil {
				t.Fatal(err)
			}
			run, err := s.store.GetRun(scopedCtx(s.org), runID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != "failure" {
				t.Fatalf("interrupted run remains %s", run.Status)
			}
		})
	}
}
