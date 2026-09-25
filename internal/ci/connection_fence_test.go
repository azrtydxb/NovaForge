package ci_test

import (
	"context"
	"google.golang.org/protobuf/proto"
	"testing"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRunnerReplacementFencesOldJobStatusAndDisconnect(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	first, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	oldJob := s.job("refs/heads/main", "old", "staging", "DEPLOY_TOKEN")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, first); err != nil {
		t.Fatal(err)
	}
	if err := s.store.StartClaimedJob(ctx, oldJob, s.runnerID); err != nil {
		t.Fatal(err)
	}
	second, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	if s.jobState(oldJob).Status != "failure" {
		t.Fatal("replacement left old job running")
	}
	server := ci.NewServer(s.store, s.dispatcher)
	for _, connection := range []string{first.String(), second.String(), ""} {
		_, err := server.ReportStatus(ctx, &civ1.ReportStatusRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, ConnectionId: connection, JobId: oldJob.String(), Status: "success", LastLogSequence: proto.Int64(0)})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("old job accepted status with connection %q: %v", connection, err)
		}
	}
	newJob := s.job("refs/heads/main", "replacement", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, second); err != nil {
		t.Fatal(err)
	}
	if err := s.store.StartClaimedJob(ctx, newJob, s.runnerID); err != nil {
		t.Fatal(err)
	}
	_ = s.store.EndRunnerConnection(ctx, s.runnerID, first)
	if _, err := server.ReportStatus(ctx, &civ1.ReportStatusRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, ConnectionId: second.String(), JobId: newJob.String(), Status: "success", LastLogSequence: proto.Int64(0)}); err != nil {
		t.Fatalf("old disconnect fenced replacement: %v", err)
	}
	if s.jobState(newJob).Status != "success" {
		t.Fatal("replacement cannot finish")
	}
}
