package gitops_test

import (
	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMergeExpectedSourceAndNativeRefLocks(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	rp, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "cas"})
	if err != nil {
		t.Fatal(err)
	}
	commit := func(branch, text string) string {
		t.Helper()
		r, e := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: rp.Repo.Id, Branch: branch, Message: text, Files: []*gitv1.FileChange{{Path: "file", Content: []byte(text)}}})
		if e != nil {
			t.Fatal(e)
		}
		return r.Sha
	}
	base := commit("main", "base")
	if _, err = srv.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: rp.Repo.Id, Name: "feature", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	head := commit("feature", "reviewed")
	req := &gitv1.MergeRequest{Repo: rp.Repo.Id, SourceRef: "feature", TargetRef: "main", ExpectedSourceSha: base}
	if _, err = srv.Merge(ctx, req); status.Code(err) != codes.Aborted {
		t.Fatalf("stale inspected head accepted: %v", err)
	}
	bare := filepath.Join(root, org.String(), "cas.git")
	if got := strings.TrimSpace(runGit(t, bare, "rev-parse", "main")); got != base {
		t.Fatalf("stale merge moved main: %s", got)
	}
	req.ExpectedSourceSha = head
	req.ExpectedTargetSha = head
	if _, err = srv.Merge(ctx, req); status.Code(err) != codes.Aborted {
		t.Fatalf("stale target policy baseline accepted: %v", err)
	}
	req.ExpectedTargetSha = base
	// Git itself holds BOTH ref locks in the prepared transaction. A transport
	// receive-pack/update-ref writer cannot move the reviewed head in this window.
	marker := filepath.Join(t.TempDir(), "prepared")
	release := marker + ".release"
	hook := "#!/bin/sh\ngrep -q ' refs/heads/main$' || exit 0\nif [ \"$1\" = prepared ]; then\n touch '" + marker + "'\n while [ ! -f '" + release + "' ]; do sleep 0.02; done\nfi\n"
	if err = os.WriteFile(filepath.Join(bare, "hooks", "reference-transaction"), []byte(hook), 0755); err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(release, nil, 0600)
	done := make(chan error, 1)
	go func() { _, e := srv.Merge(ctx, req); done <- e }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e := os.Stat(marker); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("merge never prepared native ref transaction")
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, e := exec.Command("git", "--git-dir="+bare, "update-ref", "refs/heads/feature", base, head).CombinedOutput()
	if e == nil || !strings.Contains(string(out), "cannot lock ref") {
		t.Fatalf("source was not locked: %s %v", out, e)
	}
	out, e = exec.Command("git", "--git-dir="+bare, "update-ref", "refs/heads/main", head, base).CombinedOutput()
	if e == nil || !strings.Contains(string(out), "cannot lock ref") {
		t.Fatalf("target was not locked in prepared transaction: %s %v", out, e)
	}
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("merge did not finish")
	}
	if got := strings.TrimSpace(runGit(t, bare, "rev-parse", "feature")); got != head {
		t.Fatalf("reviewed head changed: %s", got)
	}
}

// The competing writer runs after clone/check/merge/object import but before
// the real final update-ref transaction, not before the Merge RPC starts.
func TestMergeFinalTransactionRejectsMovedTarget(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	rp, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "finalcas"})
	if err != nil {
		t.Fatal(err)
	}
	commit := func(branch, text string) string {
		t.Helper()
		out, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: rp.Repo.Id, Branch: branch, Message: text, Files: []*gitv1.FileChange{{Path: "file", Content: []byte(text)}}})
		if err != nil {
			t.Fatal(err)
		}
		return out.Sha
	}
	base := commit("main", "base")
	if _, err = srv.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: rp.Repo.Id, Name: "feature", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	head := commit("feature", "reviewed")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(root, org.String(), "finalcas.git")
	wrapper := t.TempDir()
	marker := filepath.Join(wrapper, "moved")
	script := "#!/bin/sh\nif [ \"$1\" = update-ref ] && [ \"$2\" = --stdin ] && [ \"$PWD\" = '" + bare + "' ] && [ ! -f '" + marker + "' ]; then\n '" + realGit + "' update-ref refs/heads/main '" + head + "' '" + base + "' || exit 1\n touch '" + marker + "'\nfi\nexec '" + realGit + "' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = srv.Merge(ctx, &gitv1.MergeRequest{Repo: rp.Repo.Id, SourceRef: "feature", TargetRef: "main", ExpectedSourceSha: head, ExpectedTargetSha: base})
	if status.Code(err) != codes.Aborted || !strings.Contains(err.Error(), "merge ref transaction rejected") {
		t.Fatalf("final transaction accepted changed target: %v", err)
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("race never reached final native transaction")
	}
	if got := strings.TrimSpace(runGit(t, bare, "rev-parse", "main")); got != head {
		t.Fatalf("concurrent target overwritten: %s", got)
	}
}
