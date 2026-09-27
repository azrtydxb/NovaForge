package gitops_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/gitops"
)

func TestBlobOwnershipAndDurableCleanup(t *testing.T) {
	f := newLFSFixture(t, 1<<20)
	org, repo := f.newRepo(t, "owner", "payloads")
	ctx := context.Background()
	data := []byte("immutable binary content")
	oid := fmt.Sprintf("%x", sha256.Sum256(data))
	put := func(org, repo uuid.UUID) {
		t.Helper()
		if _, err := f.store.PutObject(ctx, org, repo, oid, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
	}
	read := func(org, repo uuid.UUID) {
		t.Helper()
		rc, _, err := f.store.OpenObject(ctx, org, repo, oid)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("payload changed: %q %v", got, err)
		}
	}
	put(org, repo)
	// A rejected re-upload must not overwrite or delete the first accepted object.
	if _, err := f.store.PutObject(ctx, org, repo, oid, bytes.NewReader([]byte("bad")), 3); err == nil {
		t.Fatal("accepted incorrect hash")
	}
	read(org, repo)
	srv := gitops.NewGRPCServer(f.pool, f.root)
	fork, err := srv.ForkRepo(scopedCtx(org), &gitv1.ForkRepoRequest{Repo: repo.String(), Name: "fork"})
	if err != nil {
		t.Fatal(err)
	}
	forkID := uuid.MustParse(fork.GetRepo().GetId())
	read(org, forkID)
	to := uuid.New()
	if _, err = srv.TransferRepo(scopedCtx(org), &gitv1.TransferRepoRequest{Repo: repo.String(), ToOrg: to.String()}); err != nil {
		t.Fatal(err)
	}
	read(to, repo)
	if rc, _, err := f.store.OpenObject(ctx, org, repo, oid); err == nil {
		rc.Close()
		t.Fatal("old owner retained access")
	}
	var key string
	if err = f.pool.QueryRow(ctx, `SELECT blob_key FROM gitplatform.lfs_objects WHERE repo_id=$1`, repo).Scan(&key); err != nil {
		t.Fatal(err)
	}
	collect := func() {
		t.Helper()
		c := gitops.BlobCollector{Pool: f.pool, Blobs: f.blobs}
		for i := 0; i < 20; i++ {
			more, e := c.CollectOne(authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: "test-cleanup"}))
			if e != nil {
				t.Fatal(e)
			}
			if !more {
				return
			}
		}
		t.Fatal("cleanup did not drain")
	}
	if _, err = f.pool.Exec(ctx, `DELETE FROM gitplatform.repositories WHERE id=$1 AND org_id=$2`, repo, to); err != nil {
		t.Fatal(err)
	}
	collect()
	read(org, forkID)
	if _, err = f.pool.Exec(ctx, `DELETE FROM gitplatform.repositories WHERE id=$1 AND org_id=$2`, forkID, org); err != nil {
		t.Fatal(err)
	}
	collect()
	if rc, err := f.blobs.Get(ctx, key); err == nil {
		rc.Close()
		t.Fatal("last reference removed but physical payload survived")
	}
	// Simulate a process that wrote its intent and bytes then died before publishing.
	orphan := "org/" + org.String() + "/repo/" + repo.String() + "/lfs/interrupted"
	if _, err = f.pool.Exec(ctx, `INSERT INTO gitplatform.blob_cleanup(blob_key,org_id) VALUES($1,$2)`, orphan, org); err != nil {
		t.Fatal(err)
	}
	if err = f.blobs.Put(ctx, orphan, bytes.NewReader(data), int64(len(data)), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	collect()
	if rc, err := f.blobs.Get(ctx, orphan); err == nil {
		rc.Close()
		t.Fatal("interrupted upload leaked")
	}
}

func TestBlobCleanupSurvivesStorageDisconnectAndWorkerRestart(t *testing.T) {
	f := newLFSFixture(t, 1<<20)
	org, repo := f.newRepo(t, "retry-owner", "retry-payload")
	ctx, cancel := context.WithTimeout(scopedCtx(org), 90*time.Second)
	defer cancel()
	key := "org/" + org.String() + "/repo/" + repo.String() + "/lfs/interrupted"
	if err := f.blobs.Put(ctx, key, strings.NewReader("retained"), 8, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO gitplatform.blob_cleanup(blob_key,org_id) VALUES($1,$2)`, key, org); err != nil {
		t.Fatal(err)
	}
	// Forward to real MinIO during initialization, then disconnect the network
	// endpoint. No synthetic S3 responses or in-memory storage are involved.
	upstream, err := url.Parse("http://" + os.Getenv("TEST_S3_ENDPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(upstream))
	t.Cleanup(proxy.Close)
	broken, err := blobstore.New(ctx, blobstore.Options{Endpoint: strings.TrimPrefix(proxy.URL, "http://"), AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), Bucket: f.blobs.Bucket()})
	if err != nil {
		t.Fatal(err)
	}
	proxy.CloseClientConnections()
	proxy.Close()
	first := gitops.BlobCollector{Pool: f.pool, Blobs: broken}
	if more, err := first.CollectOne(ctx); !more || err == nil {
		t.Fatalf("disconnected storage accepted: %v %v", more, err)
	}
	var attempts int
	var failure string
	if err := f.pool.QueryRow(ctx, `SELECT attempts,last_error FROM gitplatform.blob_cleanup WHERE org_id=$1 AND blob_key=$2`, org, key).Scan(&attempts, &failure); err != nil || attempts != 1 || failure == "" {
		t.Fatalf("retry intent lost: %d %q %v", attempts, failure, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE gitplatform.blob_cleanup SET available_at=now() WHERE org_id=$1 AND blob_key=$2`, org, key); err != nil {
		t.Fatal(err)
	}
	// An in-flight upload holds this row lock. A different worker must skip it.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM gitplatform.blob_cleanup WHERE org_id=$1 AND blob_key=$2 FOR UPDATE`, org, key); err != nil {
		t.Fatal(err)
	}
	restarted := gitops.BlobCollector{Pool: f.pool, Blobs: f.blobs}
	if more, err := restarted.CollectOne(ctx); more || err != nil {
		t.Fatalf("collector consumed locked upload: %v %v", more, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if more, err := restarted.CollectOne(ctx); !more || err != nil {
		t.Fatalf("restart did not reclaim: %v %v", more, err)
	}
	if rc, err := f.blobs.Get(ctx, key); err == nil {
		rc.Close()
		t.Fatal("physical payload survived retry")
	}
}
