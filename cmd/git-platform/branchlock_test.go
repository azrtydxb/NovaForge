package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/platformtest"
	"github.com/novaforge/novaforge/internal/svcauth"
)

const testHMACSecret = "branch-lock-test-secret"

// workItemDouble is an in-process stand-in for work-reviews: StartRun reads
// the Work Item it is asked to run against, and nothing else of that service.
type workItemDouble struct {
	workv1.WorkServiceClient
	item *workv1.WorkItem
}

func (w *workItemDouble) GetItem(ctx context.Context, in *workv1.GetItemRequest, _ ...grpc.CallOption) (*workv1.GetItemResponse, error) {
	if in.GetKey() != w.item.GetKey() && in.GetId() != w.item.GetId() {
		return nil, status.Error(codes.NotFound, "no such work item")
	}
	return &workv1.GetItemResponse{Item: w.item}, nil
}

func lockTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; source hack/env.sh")
	}
	if err := database.Migrate(url, "agents", agents.MigrationsFS); err != nil {
		t.Fatalf("migrate agents: %v", err)
	}
	if err := database.Migrate(url, "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform (capability): %v", err)
	}
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform (repositories): %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// startAgentRuntime serves the real agents.GRPCServer behind the same
// service-token interceptor agent-runtime uses, on a real listener, so the
// lock check crosses the same seam it does in a deployment: git-platform
// mints a token, agent-runtime verifies it and answers from its own schema.
func startAgentRuntime(t *testing.T, srv *agents.GRPCServer) agentsv1.AgentServiceClient {
	t.Helper()
	gs := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, testHMACSecret)))
	agentsv1.RegisterAgentServiceServer(gs, srv)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(l) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial agent-runtime: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return agentsv1.NewAgentServiceClient(conn)
}

func gitIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := gitIn(t, dir, args...); err != nil {
		t.Fatalf("git %s: %v — %s", strings.Join(args, " "), err, out)
	}
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+name, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	mustGit(t, dir, "add", name)
	mustGit(t, dir, "-c", "user.email=p@example.com", "-c", "user.name=Person", "commit", "-q", "-m", "add "+name)
}

// TestAgentBranchLockedDuringRun is spec S-7: a human push to an agent branch
// is rejected while a run holds it. It goes through the production path end to
// end: StartRun locks the run's branch as the run starts, git-platform's real
// capability function asks agent-runtime over gRPC with a service token, and
// unmodified git clients push over both transports. Identity and Work own real
// authenticated admission; the real Runner is held only at inference. Transport
// caller lookup remains local so both transports exercise this command's guard.
func TestAgentBranchLockedDuringRun(t *testing.T) {
	p, control := platformtest.StartWithControlledRunner(t)
	owner := p.NewUser(t, "lockowner")
	org := p.NewOrg(t, owner, "lockorg")
	repo := p.NewRepo(t, owner, org, "locked", nil)
	agentID := p.NewAgent(t, owner, org)
	item := p.NewWorkItem(t, owner, org, repo)
	run := p.StartAgentRun(t, owner, org, repo, agentID, item)
	runID := uuid.MustParse(run.GetId())
	control.Wait(t, run.GetId())

	orgID := uuid.MustParse(org.ID)
	person := authz.Scope{OrgID: orgID, ActorID: uuid.MustParse(owner.ID), ActorKind: "user"}
	personCtx := authz.WithScope(context.Background(), person)
	waitForState(t, p.AgentStore, personCtx, runID, "running")
	assertBranchLockLifecycle(t, p, personCtx, p.AsUser(owner, org), runID, item.GetId(), false)
	prefix := "agents/" + item.GetKey() + "/"
	if run.GetBranch() != prefix+"work" {
		t.Fatalf("run branch = %q, want %q", run.GetBranch(), prefix+"work")
	}

	root, repoName := p.GitRoot, repo.Name
	gitSrv := gitops.NewGRPCServer(p.Pool, root)
	caps := newCapFunc(p.Grants, newBranchLockGuard(p.Agents, platformtest.HMACSecret, repoIDResolver(p.Pool)))

	// --- HTTPS transport ---
	auth := func(ctx context.Context, user, pass, orgRef string) (authz.Scope, error) {
		if user == "agent" {
			return authz.Scope{OrgID: orgID, ActorID: uuid.MustParse(agentID), ActorKind: "agent"}, nil
		}
		return person, nil
	}
	httpSrv := httptest.NewServer(gitops.NewHTTPHandler(root, auth, caps))
	defer httpSrv.Close()
	humanURL := fmt.Sprintf("http://person:x@%s/%s/%s.git", httpSrv.Listener.Addr(), orgID, repoName)
	agentURL := fmt.Sprintf("http://agent:x@%s/%s/%s.git", httpSrv.Listener.Addr(), orgID, repoName)

	human := t.TempDir()
	mustGit(t, "", "clone", "-q", humanURL, human)
	commitFile(t, human, "person.txt", "a person's change\n")

	for _, ref := range []string{prefix + "work", prefix + "another"} {
		out, err := gitIn(t, human, "push", "origin", "HEAD:refs/heads/"+ref)
		if err == nil {
			t.Fatalf("a person's HTTPS push to %s was accepted while run %s holds it: %s", ref, runID, out)
		}
		if !strings.Contains(out, "locked") || !strings.Contains(out, runID.String()) {
			t.Fatalf("push to %s refused, but not for the lock: %s", ref, out)
		}
	}
	// A branch outside the lock is a person's ordinary write access.
	mustGit(t, human, "push", "-q", "origin", "HEAD:refs/heads/feature/person")

	// The run's own agent is not locked out of its own branch.
	agentDir := t.TempDir()
	mustGit(t, "", "clone", "-q", agentURL, agentDir)
	commitFile(t, agentDir, "agent.txt", "the agent's work\n")
	if out, err := gitIn(t, agentDir, "push", "origin", "HEAD:refs/heads/"+prefix+"work"); err != nil {
		t.Fatalf("the holding run's agent was refused its own branch: %v — %s", err, out)
	}

	// --- SSH transport ---
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	_ = clientPub
	fp := ssh.FingerprintSHA256(clientSigner.PublicKey())
	lookup := func(ctx context.Context, fingerprint, orgRef string) (authz.Scope, error) {
		if fingerprint != fp {
			return authz.Scope{}, fmt.Errorf("unknown key")
		}
		return person, nil
	}
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	sshSrv := gitops.NewSSHServer(root, hostSigner, lookup, caps)
	sshL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ssh: %v", err)
	}
	go func() { _ = sshSrv.Serve(sshL) }()
	defer sshL.Close()
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPath := t.TempDir() + "/id_ed25519"
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	_, port, _ := net.SplitHostPort(sshL.Addr().String())
	sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %s", keyPath, port)
	if _, err := gitIn(t, human, "remote", "add", "ssh", fmt.Sprintf("ssh://git@127.0.0.1/%s/%s.git", orgID, repoName)); err != nil {
		t.Fatalf("add ssh remote: %v", err)
	}
	out, err := gitIn(t, human, "-c", "core.sshCommand="+sshCmd, "push", "ssh", "HEAD:refs/heads/"+prefix+"over-ssh")
	if err == nil {
		t.Fatalf("a person's SSH push to the locked prefix was accepted: %s", out)
	}
	if !strings.Contains(out, "locked") {
		t.Fatalf("SSH push refused, but not for the lock: %s", out)
	}

	// --- the platform's own write RPCs ---
	// A person can also write a ref without pushing: the GUI and MCP create
	// branches through git-platform's gRPC. The lock holds there too.
	gitSrv.RefGuard = caps
	if _, err := gitSrv.CreateBranch(personCtx, &gitv1.CreateBranchRequest{Repo: repoName, Name: prefix + "by-api", FromRef: "feature/person"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("CreateBranch under the locked prefix by a person = %v, want PermissionDenied", err)
	}
	if _, err := gitSrv.CreateCommit(personCtx, &gitv1.CreateCommitRequest{Repo: repoName, Branch: prefix + "work", Message: "m",
		Files: []*gitv1.FileChange{{Path: "x.txt", Content: []byte("x")}}}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("CreateCommit to the locked branch by a person = %v, want PermissionDenied", err)
	}

	// --- the run ends, and the lock with it ---
	// Cancellation intent alone cannot release a live execution's lock. Stop
	// inference and join the real Runner's durable completion and owner cleanup
	// before requiring the idempotent CancelRun RPC to succeed without retries.
	control.Stop(t, run.GetId())
	if _, err := p.Agents.CancelRun(p.AsUser(owner, org), &agentsv1.CancelRunRequest{Id: runID.String()}); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	assertBranchLockLifecycle(t, p, personCtx, p.AsUser(owner, org), runID, item.GetId(), true)
	if out, err := gitIn(t, human, "push", "--force", "origin", "HEAD:refs/heads/"+prefix+"work"); err != nil {
		t.Fatalf("a person's HTTPS push after the run ended was refused: %v — %s", err, out)
	}
	if out, err := gitIn(t, human, "-c", "core.sshCommand="+sshCmd, "push", "ssh", "HEAD:refs/heads/"+prefix+"over-ssh"); err != nil {
		t.Fatalf("a person's SSH push after the run ended was refused: %v — %s", err, out)
	}
}

// TestBranchLockFailsClosed pins that a push to the agent namespace is
// refused when agent-runtime cannot be asked: an unanswerable lock check that
// let the push through would be a lock that holds only while nothing is
// wrong. Branches outside the namespace never depend on agent-runtime.
func TestBranchLockFailsClosed(t *testing.T) {
	conn, err := grpc.NewClient("127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	repoID := uuid.New()
	guard := newBranchLockGuard(agentsv1.NewAgentServiceClient(conn), testHMACSecret,
		func(context.Context, uuid.UUID, string) (uuid.UUID, error) { return repoID, nil })
	person := authz.Scope{OrgID: uuid.New(), ActorID: uuid.New(), ActorKind: "user"}

	if err := guard(context.Background(), person, person.OrgID, "r", []string{"refs/heads/agents/NF-9/work"}); err == nil {
		t.Fatal("a push to an agent branch was accepted with agent-runtime unreachable")
	}
	if err := guard(context.Background(), person, person.OrgID, "r", []string{"refs/heads/main", "refs/tags/v1"}); err != nil {
		t.Fatalf("a push outside the agent namespace depended on agent-runtime: %v", err)
	}
}

func waitForState(t *testing.T, store *agents.Store, ctx context.Context, runID uuid.UUID, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		run, err := store.GetRun(ctx, runID)
		if err == nil && run.State == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s state = %q (%v), want %q", runID, run.State, err, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
