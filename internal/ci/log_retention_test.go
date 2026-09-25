package ci_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/retention"
)

func TestRunnerLogRetentionDeleteThenCallbackFailureRestart(t *testing.T) {
	s := newCredentialStack(t)
	ctx := context.Background()
	if err := database.Migrate(dbURL(t), "retention", retention.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	job := s.job("refs/heads/main", "retention", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE ci.workflow_jobs SET status='success',finished_at=now()-interval '100 days',log_seal_pending=true WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO ci.runner_log_lines(job_id,sequence,line,content_sha256) VALUES($1,1,'durable',$2)`, job, []byte("digest")); err != nil {
		t.Fatal(err)
	}
	blobs := artifactsBlobstore(t)
	rdb := ciRedis(t)
	if err := blobs.Put(ctx, "logs/"+job.String()+".txt", strings.NewReader("durable\n"), 8, "text/plain"); err != nil {
		t.Fatal(err)
	}
	svc := ci.NewService(s.pool, rdb, blobs, &stubGitClient{}, "test", "")
	original := svc.Sweeper.AfterLogDelete
	svc.Sweeper.AfterLogDelete = func(ctx context.Context, id retention.LogIdentity) error {
		if id.JobID == job {
			return errors.New("injected callback failure")
		}
		return original(ctx, id)
	}
	if _, err := svc.Sweeper.Sweep(ctx); err == nil {
		t.Fatal("callback failure hidden")
	}
	var retired, pending bool
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT logs_retired,log_seal_pending FROM ci.workflow_jobs WHERE id=$1`, job).Scan(&retired, &pending); err != nil || !retired || pending {
		t.Fatalf("materialization not fenced: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ci.runner_log_lines WHERE job_id=$1`, job).Scan(&count); err != nil || count != 1 {
		t.Fatal("callback failure lost payload")
	}
	id := retention.LogIdentity{OrgID: s.org, JobID: job, ConnectionID: &connection}
	foreign := authz.WithScope(ctx, authz.Scope{OrgID: uuid.New(), ActorKind: "service", ServiceName: "ci-retention"})
	if err := original(foreign, id); err == nil {
		t.Fatal("foreign retention callback accepted")
	}
	owner := authz.WithScope(ctx, authz.Scope{OrgID: s.org, ActorKind: "service", ServiceName: "ci-retention"})
	wrong := uuid.New()
	id.ConnectionID = &wrong
	if err := original(owner, id); err == nil {
		t.Fatal("wrong generation accepted")
	}
	// New composition discovers the same durable retired inventory even though
	// deletion succeeded and the first callback never acknowledged it.
	fresh := ci.NewService(s.pool, rdb, blobs, &stubGitClient{}, "test", "")
	if _, err := fresh.Sweeper.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ci.runner_log_lines WHERE job_id=$1`, job).Scan(&count); err != nil || count != 0 {
		t.Fatal("restart did not clear retired payload")
	}
	if _, err := fresh.Logs.Seal(ctx, job); err == nil {
		t.Fatal("retired log materialized again")
	}
	if _, err := fresh.Sweeper.Sweep(ctx); err != nil {
		t.Fatal("idempotent retention retry failed", err)
	}
}
