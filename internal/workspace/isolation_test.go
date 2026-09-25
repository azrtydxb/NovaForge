package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/mcp"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/workspace"
)

// TestAgentRunIsolationAndEvidence is spec S-7: an Agent Run executes in its
// own Kubernetes namespace, and its Git changes, events and evidence persist
// after the namespace is destroyed.
//
// Against the real cluster (KUBE_CONTEXT), not client-go's fake, which
// validates nothing and accepted two workspaces the API server refused. A run
// is provisioned; its namespace, network policy and pod are observed on the
// cluster; the run writes a file in the pod, commits it through the git
// service, records a tool call and settles. Then the namespace is destroyed
// and observed gone — and the commit, the audit entries, the stored state and
// the state events on the stream are all read back afterwards.
func TestAgentRunIsolationAndEvidence(t *testing.T) {
	kubeContext := os.Getenv("KUBE_CONTEXT")
	dbURL := os.Getenv("TEST_DATABASE_URL")
	redisURL := os.Getenv("TEST_REDIS_URL")
	if kubeContext == "" || dbURL == "" || redisURL == "" {
		t.Skip("KUBE_CONTEXT, TEST_DATABASE_URL and TEST_REDIS_URL are needed; source hack/env.sh to run against the cluster")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		t.Fatalf("load kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	if err := database.Migrate(dbURL, "agents", agents.MigrationsFS); err != nil {
		t.Fatalf("migrate agents: %v", err)
	}
	if err := database.MigrateAs(dbURL, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform: %v", err)
	}
	if err := database.Migrate(dbURL, "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatalf("migrate capabilities: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	orgID := uuid.New()
	personCtx := authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"})
	gitSrv := gitops.NewGRPCServer(pool, t.TempDir())
	repoName := "isolated-" + uuid.NewString()[:8]
	if _, err := gitSrv.CreateRepo(personCtx, &gitv1.CreateRepoRequest{Name: repoName}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	store := agents.NewStore(pool)
	audit := agents.NewAuditLog(pool)
	agent, err := store.CreateAgent(personCtx, agents.Agent{OrgID: orgID, Name: "iso-" + uuid.NewString()[:8], Role: "engineer", ModelRef: "m", Enabled: true})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	// The run writes through the git API as its agent, so it holds a real
	// capability grant for its branch, and git-platform checks it.
	grants := capability.NewStore(pool)
	store.GrantRevoker = grants.Revoke
	gitSrv.Grants = grants
	grant, err := grants.Issue(personCtx, capability.Grant{OrgID: orgID, SubjectID: agent.ID, SubjectKind: "agent",
		RepoRead: true, WriteBranch: "agents/NF-1/", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Issue grant: %v", err)
	}
	run, err := store.CreateRun(personCtx, agents.Run{OrgID: orgID, AgentID: agent.ID, SponsorID: uuid.New(), GrantID: grant.ID,
		Branch: "agents/NF-1/work", WallclockLimit: time.Hour})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := store.SetRunState(personCtx, run.ID, "running"); err != nil {
		t.Fatalf("running: %v", err)
	}

	// --- the run's own namespace, on the cluster ---
	p := workspace.NewProvisioner(client).WithRESTConfig(cfg).WithCleanupRecorder(store.RecordWorkspaceCleanup)
	store.WorkspaceCleaner = p.DestroyConfirmed
	ws, err := p.Create(ctx, run.ID, workspace.Spec{OrgID: orgID, Image: workspace.DefaultImage, CPULimit: "1", MemLimit: "1Gi"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = p.Destroy(context.Background(), run.ID) })
	ns, err := client.CoreV1().Namespaces().Get(ctx, ws.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the run's namespace %s is not on the cluster: %v", ws.Namespace, err)
	}
	if !strings.Contains(ns.Name, run.ID.String()) {
		t.Fatalf("namespace %s does not belong to run %s", ns.Name, run.ID)
	}
	policies, err := client.NetworkingV1().NetworkPolicies(ws.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(policies.Items) == 0 {
		t.Fatalf("the run's namespace has no network policy (%v)", err)
	}
	if err := p.WaitReady(ctx, run.ID, 5*time.Minute); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	// A policy object's existence is not confinement. Probe the reachable API
	// destination from the real pod, and check no cluster token was mounted.
	apiURL, err := url.Parse(cfg.Host)
	if err != nil {
		t.Fatal(err)
	}
	port := apiURL.Port()
	if port == "" {
		port = "443"
	}
	probe, err := p.Exec(ctx, run.ID, []string{"timeout", "2", "bash", "-c", `exec 3<>/dev/tcp/$1/$2`, "bash", apiURL.Hostname(), port}, nil)
	if err != nil || probe.ExitCode != 124 {
		t.Fatalf("workspace API egress was not blocked by timeout: exit=%d stderr=%s err=%v", probe.ExitCode, probe.Stderr, err)
	}
	probe, err = p.Exec(ctx, run.ID, []string{"sh", "-c", "test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token"}, nil)
	if err != nil || probe.ExitCode != 0 {
		t.Fatalf("workspace has service-account token: %+v, %v", probe, err)
	}

	// The external stdio process is launched over Kubernetes exec in this
	// very sandbox, never on agent-runtime's credentialed host.
	stream, err := p.OpenStdio(personCtx, run.ID, []string{"sh", "-c", `
setsid sh -c 'while :; do date +%s > /workspace/detached-heartbeat; sleep 1; done' </dev/null >/dev/null 2>&1 &
echo $! > /workspace/detached-pid
read -r line
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}'
read -r line
read -r line
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"sandbox","inputSchema":{"type":"object"}}]}}'
read -r line
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"confined"}]}}'
# A normal session stays alive through authorized artifact collection.
cat >/dev/null
`})
	if err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewStdioClient(mcp.ServerDef{Name: "sandbox"}, []string{"sandbox"}, stream)
	defer mcpClient.Close()
	if err := mcpClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defs, err := mcpClient.ListTools(ctx)
	if err != nil || len(defs) != 1 {
		t.Fatalf("stdio discovery: %+v, %v", defs, err)
	}
	answer, err := mcpClient.Call(ctx, "sandbox", []byte(`{}`))
	if err != nil || answer.Content != "confined" || !answer.Untrusted {
		t.Fatalf("stdio call: %+v, %v", answer, err)
	}

	// --- the run does its work inside the namespace ---
	const content = "written inside the run's own namespace\n"
	if _, err := p.Exec(ctx, run.ID, []string{"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", workspace.Root + "/NOTES.md"},
		strings.NewReader(content)); err != nil {
		t.Fatalf("write in the workspace: %v", err)
	}
	res, err := p.Exec(ctx, run.ID, []string{"cat", workspace.Root + "/NOTES.md"}, nil)
	if err != nil || string(res.Stdout) != content {
		t.Fatalf("read back from the workspace = %q, %v", res.Stdout, err)
	}
	runCtx := authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorID: agent.ID, ActorKind: "agent"})
	args, _ := json.Marshal(map[string]any{"repo": repoName, "branch": "agents/NF-1/work", "files": []string{"NOTES.md"}})
	callID, err := audit.Record(runCtx, agents.Entry{RunID: run.ID, Tool: "git.commit", ArgsJSON: args})
	if err != nil {
		t.Fatalf("audit Record: %v", err)
	}
	commit, err := gitSrv.CreateCommit(runCtx, &gitv1.CreateCommitRequest{Repo: repoName, Branch: "agents/NF-1/work", Message: "notes from the run",
		Files: []*gitv1.FileChange{{Path: "NOTES.md", Content: res.Stdout}}})
	if err != nil {
		t.Fatalf("CreateCommit: %v", err)
	}
	if err := audit.Complete(runCtx, callID, "ok", ""); err != nil {
		t.Fatalf("audit Complete: %v", err)
	}
	// Normal closure happens only after authorized artifact collection. The
	// detached session escaped its exec shell, so stream close alone cannot stop it.
	probe, err = p.Exec(ctx, run.ID, []string{"sh", "-c", "kill -0 $(cat /workspace/detached-pid)"}, nil)
	if err != nil || probe.ExitCode != 0 {
		t.Fatalf("detached child not running before Close: %+v %v", probe, err)
	}
	if err = mcpClient.Close(); err != nil {
		t.Fatalf("confirmed stdio teardown: %v", err)
	}
	if _, err = p.Exec(ctx, run.ID, []string{"true"}, nil); !errors.Is(err, workspace.ErrWorkspaceClosed) {
		t.Fatalf("stdio Close allowed later workspace work: %v", err)
	}
	saved, err := store.GetRun(personCtx, run.ID)
	if err != nil || saved.WorkspaceIdentity == nil || !saved.WorkspaceIdentity.TerminationObserved {
		t.Fatalf("no durable termination evidence: %+v %v", saved, err)
	}
	if err = store.ConfirmWorkspaceCleanup(personCtx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := agents.SettleRun(runCtx, store, rdb, run, "succeeded"); err != nil {
		t.Fatalf("SettleRun: %v", err)
	}

	// --- the namespace is destroyed, and is gone ---
	if err := p.Destroy(ctx, run.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for {
		_, err := client.CoreV1().Namespaces().Get(ctx, ws.Namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("namespace %s still present after Destroy (last error %v)", ws.Namespace, err)
		}
		time.Sleep(2 * time.Second)
	}

	// --- and everything the run left is still there ---
	blob, err := gitSrv.GetBlob(personCtx, &gitv1.GetBlobRequest{Repo: repoName, Ref: "agents/NF-1/work", Path: "NOTES.md"})
	if err != nil || string(blob.GetContent()) != content {
		t.Fatalf("the run's commit %s after teardown = %q, %v", commit.GetSha(), blob.GetContent(), err)
	}
	entries, err := audit.List(personCtx, run.ID)
	if err != nil || len(entries) != 1 || entries[0].Tool != "git.commit" || entries[0].Outcome != "ok" {
		t.Fatalf("audit entries after teardown = %+v, %v", entries, err)
	}
	stored, err := store.GetRun(personCtx, run.ID)
	if err != nil || stored.State != "succeeded" || stored.EndedAt == nil {
		t.Fatalf("stored run after teardown = %+v, %v", stored, err)
	}
	msgs, err := rdb.XRevRangeN(ctx, events.StreamAgentEvents, "+", "-", 5000).Result()
	if err != nil {
		t.Fatalf("read agent events: %v", err)
	}
	var sawEnd bool
	for _, m := range msgs {
		raw, _ := m.Values["data"].(string)
		var evt events.AgentEvent
		if json.Unmarshal([]byte(raw), &evt) == nil && evt.RunID == run.ID && evt.ToState == "succeeded" {
			sawEnd = true
			break
		}
	}
	if !sawEnd {
		t.Fatal("the run's terminal state change is not on the agent event stream after teardown")
	}
}
