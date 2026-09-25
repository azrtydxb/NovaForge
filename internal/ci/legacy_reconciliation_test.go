package ci_test

import (
	"context"
	"fmt"
	"github.com/novaforge/novaforge/internal/blobstore"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/ci"
)

func TestLegacySealQueueAcknowledgesAndReachesNewerJournal(t *testing.T) {
	s := newCredentialStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	old, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.store.CreateRun(ctx, ci.Run{OrgID: s.org, RepoID: s.repo, CommitSHA: uuid.NewString(), Ref: "main", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	var first uuid.UUID
	for i := 0; i < 100; i++ {
		job, err := s.store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: fmt.Sprintf("legacy-%03d", i), RunCmd: "true"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = job.ID
		}
	}
	// These rows model the predecessor version: connection-aware but not
	// journal-enabled. The real replacement call creates their seal obligations.
	if _, err := s.pool.Exec(ctx, `UPDATE ci.workflow_jobs SET status='running',runner_id=$2,connection_id=$3 WHERE run_id=$1`, run.ID, s.runnerID, old); err != nil {
		t.Fatal(err)
	}
	connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	newer := s.job("refs/heads/main", "journal", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE ci.workflow_jobs SET status='success',finished_at=now(),log_seal_pending=true WHERE id=$1`, newer); err != nil {
		t.Fatal(err)
	}
	blobs := artifactsBlobstore(t)
	rdb := ciRedis(t)
	svc := ci.NewService(s.pool, rdb, blobs, &stubGitClient{}, "test", "")
	if err := svc.Logs.Append(ctx, first, "legacy evidence"); err != nil {
		t.Fatal(err)
	}
	svc.Run(ctx)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		var pending int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ci.workflow_jobs j JOIN ci.workflow_runs r ON r.id=j.run_id WHERE r.org_id=$1 AND log_seal_pending`, s.org).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			body, err := blobs.Get(ctx, "logs/"+first.String()+".txt")
			if err != nil {
				t.Fatal(err)
			}
			body.Close()
			body, err = blobs.Get(ctx, "logs/"+newer.String()+".txt")
			if err != nil {
				t.Fatal(err)
			}
			body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("legacy successful seals retained obligations and starved newer journal")
}

func TestFailedLegacySealClaimsDoNotStarveNewerJournal(t *testing.T) {
	s := newCredentialStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	old, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.store.CreateRun(ctx, ci.Run{OrgID: s.org, RepoID: s.repo, CommitSHA: uuid.NewString(), Ref: "main", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	var first uuid.UUID
	blocked := map[string]bool{}
	for i := 0; i < 100; i++ {
		job, err := s.store.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: fmt.Sprintf("legacy-%03d", i), RunCmd: "true"})
		if err != nil {
			t.Fatal(err)
		}
		blocked[job.ID.String()+".txt"] = true
		if i == 0 {
			first = job.ID
		}
	}
	// These rows model the predecessor version: connection-aware but not
	// journal-enabled. The real replacement call creates their seal obligations.
	if _, err := s.pool.Exec(ctx, `UPDATE ci.workflow_jobs SET status='running',runner_id=$2,connection_id=$3 WHERE run_id=$1`, run.ID, s.runnerID, old); err != nil {
		t.Fatal(err)
	}
	connection, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	newer := s.job("refs/heads/main", "journal", "staging")
	if _, _, err := s.store.ClaimForDispatch(ctx, s.runnerID, nil, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE ci.workflow_jobs SET status='success',finished_at=now(),log_seal_pending=true WHERE id=$1`, newer); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse("http://" + os.Getenv("TEST_S3_ENDPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if blocked[key] {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code></Error>"))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	blobs, err := blobstore.New(ctx, blobstore.Options{Endpoint: strings.TrimPrefix(front.URL, "http://"), AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), Bucket: "nf-ci-seal-fair-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatal(err)
	}
	rdb := ciRedis(t)
	t.Cleanup(func() { rdb.Del(context.Background(), "joblog:"+first.String()) })
	svc := ci.NewService(s.pool, rdb, blobs, &stubGitClient{}, "test", "")
	if err := svc.Logs.Append(ctx, first, "legacy evidence"); err != nil {
		t.Fatal(err)
	}
	svc.Run(ctx)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		var pending bool
		if err := s.pool.QueryRow(ctx, `SELECT log_seal_pending FROM ci.workflow_jobs WHERE id=$1`, newer).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if !pending {
			var stillPending int
			if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ci.workflow_jobs WHERE run_id=$1 AND log_seal_pending`, run.ID).Scan(&stillPending); err != nil || stillPending != 100 {
				t.Fatalf("failed obligations lost: %d %v", stillPending, err)
			}
			object, err := blobs.Get(ctx, "logs/"+newer.String()+".txt")
			if err != nil {
				t.Fatal(err)
			}
			object.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("failed legacy seals starved newer journal")
}
