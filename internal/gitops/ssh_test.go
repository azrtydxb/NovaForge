package gitops_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gitops"
)

func newEd25519Signer(t *testing.T) (ssh.Signer, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer from key: %v", err)
	}
	return signer, pub, priv
}

// writePEMKey writes an OpenSSH-format private key file for use with `ssh`'s
// -i flag via `git -c core.sshCommand`.
func writePEMKey(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	path := t.TempDir() + "/id_ed25519"
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return path
}

func startSSHServer(t *testing.T, root string, lookup gitops.FingerprintFunc, caps gitops.CapFunc) (addr string, hostSigner ssh.Signer) {
	t.Helper()
	hostSigner, _, _ = newEd25519Signer(t)

	srv := gitops.NewSSHServer(root, hostSigner, lookup, caps)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(l)
	t.Cleanup(func() { l.Close() })
	return l.Addr().String(), hostSigner
}

func TestCloneOverSSH(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, "sshrepo"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Seed a commit on main via a local clone/push isn't available yet
	// (SSH not built); build the commit directly with commit-tree instead.
	seedCommit(t, root, orgID, "sshrepo")

	clientSigner, clientPub, clientPriv := newEd25519Signer(t)
	_ = clientPub

	registeredFP := ssh.FingerprintSHA256(clientSigner.PublicKey())
	actorID := uuid.New()
	lookup := func(ctx context.Context, fingerprint, orgRef string) (authz.Scope, error) {
		if fingerprint != registeredFP {
			return authz.Scope{}, fmt.Errorf("permission denied")
		}
		return authz.Scope{OrgID: orgID, ActorID: actorID, ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		return nil
	}

	addr, _ := startSSHServer(t, root, lookup, caps)
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	keyPath := writePEMKey(t, clientPriv)
	work := t.TempDir()
	sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %d", keyPath, port)
	cloneURL := fmt.Sprintf("ssh://git@127.0.0.1/%s/sshrepo.git", orgID.String())

	cmd := exec.Command("git", "-c", "core.sshCommand="+sshCmd, "clone", cloneURL, work+"/clone")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone over ssh failed: %v — %s", err, out)
	}

	logOut, err := exec.Command("git", "-C", work+"/clone", "log", "--format=%s", "-n", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v — %s", err, logOut)
	}
	if !strings.Contains(string(logOut), "seed commit") {
		t.Fatalf("want cloned log to contain 'seed commit', got %q", logOut)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, "guarded"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedCommit(t, root, orgID, "guarded")

	lookup := func(ctx context.Context, fingerprint, orgRef string) (authz.Scope, error) {
		return authz.Scope{}, fmt.Errorf("permission denied")
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		return nil
	}
	addr, _ := startSSHServer(t, root, lookup, caps)
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	_, _, clientPriv := newEd25519Signer(t)
	keyPath := writePEMKey(t, clientPriv)
	work := t.TempDir()
	sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -p %d", keyPath, port)
	cloneURL := fmt.Sprintf("ssh://git@127.0.0.1/%s/guarded.git", orgID.String())

	cmd := exec.Command("git", "-c", "core.sshCommand="+sshCmd, "clone", cloneURL, work+"/clone")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("want clone to fail, got success: %s", out)
	}
	if !strings.Contains(string(out), "permission denied") && !strings.Contains(strings.ToLower(string(out)), "permission denied") {
		t.Fatalf("want stderr to contain 'permission denied', got %s", out)
	}
}

func TestNonGitCommandRejected(t *testing.T) {
	root := t.TempDir()
	clientSigner, _, _ := newEd25519Signer(t)
	registeredFP := ssh.FingerprintSHA256(clientSigner.PublicKey())
	lookup := func(ctx context.Context, fingerprint, orgRef string) (authz.Scope, error) {
		if fingerprint != registeredFP {
			return authz.Scope{}, fmt.Errorf("permission denied")
		}
		return authz.Scope{OrgID: uuid.New(), ActorID: uuid.New(), ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		return nil
	}
	addr, _ := startSSHServer(t, root, lookup, caps)

	config := &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	err = session.Run("/bin/sh")
	if err == nil {
		t.Fatal("want non-zero exit for non-git command")
	}
	exitErr, ok := err.(*ssh.ExitError)
	if !ok {
		t.Fatalf("want *ssh.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitStatus() == 0 {
		t.Fatalf("want non-zero exit status, got %d", exitErr.ExitStatus())
	}
}

func TestPushOverSSH(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, "pushrepo"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	clientSigner, _, clientPriv := newEd25519Signer(t)
	registeredFP := ssh.FingerprintSHA256(clientSigner.PublicKey())
	actorID := uuid.New()
	lookup := func(ctx context.Context, fingerprint, orgRef string) (authz.Scope, error) {
		if fingerprint != registeredFP {
			return authz.Scope{}, fmt.Errorf("permission denied")
		}
		return authz.Scope{OrgID: orgID, ActorID: actorID, ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		return nil
	}
	addr, _ := startSSHServer(t, root, lookup, caps)
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	keyPath := writePEMKey(t, clientPriv)
	work := t.TempDir()
	sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %d", keyPath, port)
	cloneURL := fmt.Sprintf("ssh://git@127.0.0.1/%s/pushrepo.git", orgID.String())

	if out, err := exec.Command("git", "-c", "core.sshCommand="+sshCmd, "clone", cloneURL, work).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v — %s", err, out)
	}
	if err := os.WriteFile(work+"/f.txt", []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitCmds(t, work, sshCmd,
		[]string{"add", "f.txt"},
		[]string{"-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "push over ssh"},
		[]string{"push", "origin", "HEAD:refs/heads/main"},
	)

	repo, err := gitops.Open(root, orgID, "pushrepo")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	log, err := repo.Log("main", 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	found := false
	for _, c := range log {
		if c.Message == "push over ssh" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want pushed commit in log, got %+v", log)
	}
}

func TestPushOverSSHDeniedByCapability(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, "guardedssh"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	clientSigner, _, clientPriv := newEd25519Signer(t)
	registeredFP := ssh.FingerprintSHA256(clientSigner.PublicKey())
	lookup := func(ctx context.Context, fingerprint, orgRef string) (authz.Scope, error) {
		if fingerprint != registeredFP {
			return authz.Scope{}, fmt.Errorf("permission denied")
		}
		return authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		for _, r := range refs {
			if r == "refs/heads/main" {
				return fmt.Errorf("write to %s not permitted", r)
			}
		}
		return nil
	}
	addr, _ := startSSHServer(t, root, lookup, caps)
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	keyPath := writePEMKey(t, clientPriv)
	work := t.TempDir()
	sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %d", keyPath, port)
	cloneURL := fmt.Sprintf("ssh://git@127.0.0.1/%s/guardedssh.git", orgID.String())

	if out, err := exec.Command("git", "-c", "core.sshCommand="+sshCmd, "clone", cloneURL, work).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v — %s", err, out)
	}
	if err := os.WriteFile(work+"/f.txt", []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitCmds(t, work, sshCmd,
		[]string{"add", "f.txt"},
		[]string{"-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "push over ssh"},
	)

	cmd := exec.Command("git", "-c", "core.sshCommand="+sshCmd, "push", "origin", "HEAD:refs/heads/main")
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("want push to fail, got success: %s", out)
	}
	if !strings.Contains(string(out), "not permitted") {
		t.Fatalf("want stderr to contain 'not permitted', got %s", out)
	}
}

func runGitCmds(t *testing.T, dir, sshCmd string, argSets ...[]string) {
	t.Helper()
	for _, args := range argSets {
		full := append([]string{"-c", "core.sshCommand=" + sshCmd}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v — %s", strings.Join(full, " "), err, out)
		}
	}
}

// seedCommit builds one commit directly on refs/heads/main of the bare repo
// at root/orgID/name.git using plumbing commands, without a working tree.
func seedCommit(t *testing.T, root string, orgID uuid.UUID, name string) {
	t.Helper()
	repo, err := gitops.Open(root, orgID, name)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gitDir := repo.Path()

	run := func(args ...string) string {
		full := append([]string{"--git-dir=" + gitDir}, args...)
		cmd := exec.Command("git", full...)
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s: %v — %s", strings.Join(full, " "), err, stderr.String())
		}
		return out.String()
	}
	runStdin := func(stdin string, args ...string) string {
		full := append([]string{"--git-dir=" + gitDir}, args...)
		cmd := exec.Command("git", full...)
		cmd.Stdin = strings.NewReader(stdin)
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s: %v — %s", strings.Join(full, " "), err, stderr.String())
		}
		return out.String()
	}

	emptyTree := strings.TrimSpace(runStdin("", "mktree"))
	commitSHA := strings.TrimSpace(runStdin("", "commit-tree", emptyTree, "-m", "seed commit"))
	run("update-ref", "refs/heads/main", commitSHA)
}
