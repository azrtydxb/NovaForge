package ci

import (
	"context"
	"crypto/sha256"
	"google.golang.org/protobuf/proto"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/database"
)

func journalFixture(t *testing.T) (*Store, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "ci", MigrationsFS); err != nil {
		t.Fatal(err)
	}
	p, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	s := NewStore(p)
	ctx := context.Background()
	org := uuid.New()
	runner, err := s.RegisterRunner(ctx, org, "journal", nil, []byte("test-hash"))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.BeginRunnerConnection(ctx, runner)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.CreateRun(ctx, Run{OrgID: org, RepoID: uuid.New(), CommitSHA: uuid.NewString(), Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateJob(ctx, WorkflowJob{RunID: run.ID, Name: "journal", RunCmd: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimForDispatch(ctx, runner, nil, connection); err != nil {
		t.Fatal(err)
	}
	if err := s.StartClaimedJob(ctx, job.ID, runner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DeleteRun(ctx, run.ID); _, _ = p.Exec(ctx, `DELETE FROM ci.runners WHERE id=$1`, runner) })
	return s, runner, connection, job.ID
}

func TestRunnerJournalBarrierReplayAndReplacement(t *testing.T) {
	s, runner, connection, job := journalFixture(t)
	ctx := context.Background()
	req := &civ1.ReportStatusRequest{RunnerId: runner.String(), ConnectionId: connection.String(), JobId: job.String(), Status: "success", LastLogSequence: proto.Int64(1)}
	if err := s.recordRunnerReceipt(ctx, runner, connection, job, req, ""); err != errLogBarrier {
		t.Fatalf("missing log barrier accepted: %v", err)
	}
	digest := sha256.Sum256([]byte("private"))
	if err := s.appendRunnerLog(ctx, runner, connection, job, 1, "***", digest); err != nil {
		t.Fatal(err)
	}
	if err := s.recordRunnerReceipt(ctx, runner, connection, job, req, ""); err != nil {
		t.Fatal(err)
	}
	// Losing masking context does not break an exact frame replay: only its
	// digest and original stored redaction matter, never replacement text.
	if err := s.appendRunnerLog(ctx, runner, connection, job, 1, SuppressedCredentialOutput, digest); err != nil {
		t.Fatal(err)
	}
	if err := s.appendRunnerLog(ctx, runner, connection, job, 1, "forged", sha256.Sum256([]byte("other"))); err == nil {
		t.Fatal("conflicting log replay accepted")
	}
	if err := s.appendRunnerLog(ctx, runner, connection, job, 2, "late", digest); err == nil {
		t.Fatal("terminal log admission reopened")
	}
	if _, err := s.BeginRunnerConnection(ctx, runner); err != nil {
		t.Fatal(err)
	}
	if err := s.appendRunnerLog(ctx, runner, connection, job, 1, "***", digest); err == nil {
		t.Fatal("old connection replay admitted after replacement")
	}
	lines, _, err := s.journalSnapshot(ctx, job)
	if err != nil || len(lines) != 1 || lines[0] != "***" {
		t.Fatalf("journal changed: %v %v", lines, err)
	}
}

func TestRunnerJournalReplacementWaitsForAdmittedTransaction(t *testing.T) {
	s, runner, connection, job := journalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, _, _, err := lockRunnerJob(ctx, tx, runner, connection, job); err != nil {
		t.Fatal(err)
	}
	// This is the exact production admission lock; replacement cannot pass it.
	replaced := make(chan error, 1)
	go func() { _, err := s.BeginRunnerConnection(ctx, runner); replaced <- err }()
	if _, err := tx.Exec(ctx, `INSERT INTO ci.runner_log_lines(job_id,sequence,line,content_sha256) VALUES($1,1,'admitted',$2)`, job, []byte("digest")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ci.workflow_jobs SET log_sequence=1 WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	if err := s.appendRunnerLog(ctx, runner, connection, job, 2, "old", sha256.Sum256(nil)); err == nil {
		t.Fatal("old frame escaped committed replacement")
	}
	lines, _, err := s.journalSnapshot(ctx, job)
	if err != nil || len(lines) != 1 || lines[0] != "admitted" {
		t.Fatalf("accepted evidence lost: %v %v", lines, err)
	}
}
