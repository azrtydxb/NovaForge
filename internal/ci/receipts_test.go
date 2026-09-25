package ci_test

import (
	"context"
	"github.com/google/uuid"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

func TestRunnerTerminalReceiptImmutable(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	job := s.job("refs/heads/main", "receipt", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	if err := s.store.StartClaimedJob(ctx, job, s.runnerID); err != nil {
		t.Fatal(err)
	}
	server := ci.NewServer(s.store, s.dispatcher)
	req := &civ1.ReportStatusRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, ConnectionId: connection.String(), JobId: job.String(), Status: "success", LastLogSequence: proto.Int64(0), Detail: "first receipt"}
	if _, err := server.ReportStatus(ctx, req); err != nil {
		t.Fatal(err)
	}
	before := s.jobState(job)
	if _, err := server.ReportStatus(ctx, req); err != nil {
		t.Fatal("identical receipt rejected", err)
	}
	after := s.jobState(job)
	if before.FinishedAt == nil || after.FinishedAt == nil || !before.FinishedAt.Equal(*after.FinishedAt) {
		t.Fatal("identical terminal receipt changed finish time")
	}
	req.Status = "failure"
	if _, err := server.ReportStatus(ctx, req); err == nil {
		t.Fatal("conflicting terminal receipt overwrote success")
	}
	if s.jobState(job).Status != "success" {
		t.Fatal("terminal outcome changed")
	}
}

type receiptSealBarrier struct {
	sink    *ci.LogSink
	entered chan struct{}
	release chan struct{}
}

func (s *receiptSealBarrier) Append(ctx context.Context, job uuid.UUID, line string) error {
	return s.sink.Append(ctx, job, line)
}
func (s *receiptSealBarrier) Seal(ctx context.Context, job uuid.UUID) (string, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.release:
	}
	return s.sink.Seal(ctx, job)
}

func TestRunnerSealProjectsReceiptBeforeReplacement(t *testing.T) {
	s := newCredentialStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	job := s.job("refs/heads/main", "seal", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	if err := s.store.StartClaimedJob(ctx, job, s.runnerID); err != nil {
		t.Fatal(err)
	}
	logs := ci.NewLogSink(ciRedis(t), artifactsBlobstore(t))
	server := ci.NewServer(s.store, s.dispatcher)
	server.SetLogSink(logs)
	barrier := &receiptSealBarrier{sink: logs, entered: make(chan struct{}), release: make(chan struct{})}
	server.SetLogSink(barrier)
	result := make(chan error, 1)
	go func() {
		_, err := server.ReportStatus(ctx, &civ1.ReportStatusRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, ConnectionId: connection.String(), JobId: job.String(), Status: "success", LastLogSequence: proto.Int64(0)})
		result <- err
	}()
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	state := s.jobState(job).Status
	// Replacement must not be blocked by an open transaction across sealing.
	_, err = s.store.BeginRunnerConnection(ctx, s.runnerID)
	close(barrier.release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if state != "success" {
		t.Fatal("seal ran before durable terminal admission")
	}
	if s.jobState(job).Status != "success" {
		t.Fatal("replacement changed accepted receipt")
	}
	var pending bool
	if err := s.pool.QueryRow(ctx, `SELECT log_seal_pending FROM ci.workflow_jobs WHERE id=$1`, job).Scan(&pending); err != nil || pending {
		t.Fatalf("projection not acknowledged: %v", err)
	}
}
