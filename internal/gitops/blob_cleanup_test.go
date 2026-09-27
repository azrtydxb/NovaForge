package gitops_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
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
