package agentrun_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"io"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/mcp"
	"github.com/novaforge/novaforge/internal/workspace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktest "k8s.io/client-go/testing"
)

type lifecycleTestStream struct {
	io.ReadWriteCloser
	closeOnce sync.Once
	closeFn   func() error
	err       error
	// Discovery-failure tests hold the session open until Close. Exit tests use
	// the actual workspace transport; no fabricated exit signal proves R2.
}

func (s *lifecycleTestStream) OnExit(func()) {}
func (s *lifecycleTestStream) Close() error {
	s.closeOnce.Do(func() { _ = s.ReadWriteCloser.Close(); s.err = s.closeFn() })
	return s.err
}

func TestFailedMCPDiscoveryRetainsWorkspaceCleanup(t *testing.T) {
	p := newPlatform(t)
	for _, discovery := range []string{"initialize-fails", "no-selected-tool"} {
		for _, failure := range []string{"delete-fails", "absence-check-fails"} {
			t.Run(discovery+"/"+failure, func(t *testing.T) {
				o := p.newOrg(t)
				repo := p.repo(t, o, map[string]string{".novaforge/agents/engineer.yaml": "name: engineer\nrole: engineer\ntools: [mcp.selected.lookup]\n"})
				run := p.startRun(t, o, repo, "engineer", "read", nil)
				identity := workspace.Identity{OrgID: o.id, RunID: run.ID, NamespaceUID: "ns-uid", PodUID: "pod-uid", NodeName: "node"}
				ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nf-run-" + run.ID.String(), UID: identity.NamespaceUID, Labels: map[string]string{"novaforge.io/org-id": o.id.String(), "novaforge.io/run-id": run.ID.String()}}}
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: ns.Name, UID: identity.PodUID}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "agent"}}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "agent", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}
				client := fake.NewClientset(ns, pod)
				var deleteAttempt atomic.Bool
				var failing atomic.Bool
				failing.Store(true)
				client.PrependReactor("delete", "namespaces", func(ktest.Action) (bool, runtime.Object, error) {
					if !failing.Load() {
						return false, nil, nil
					}
					deleteAttempt.Store(true)
					if failure == "delete-fails" {
						return true, nil, fmt.Errorf("namespace deletion unavailable")
					}
					return true, nil, nil
				})
				client.PrependReactor("get", "namespaces", func(ktest.Action) (bool, runtime.Object, error) {
					if failing.Load() && deleteAttempt.Load() && failure == "absence-check-fails" {
						return true, nil, fmt.Errorf("absence confirmation unavailable")
					}
					return false, nil, nil
				})
				provisioner := workspace.NewProvisioner(client).WithCleanupRecorder(p.agents.RecordWorkspaceCleanup)
				p.agents.WorkspaceCleaner = provisioner.DestroyConfirmed
				model := &recordingModel{script: []stubResponse{{text: "done"}}}
				var requested []string
				runner := p.runner(model, &requested)
				runner.MCP = stdioRegister{}
				runner.MCPOptions.OpenStdio = func(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
					if err := p.agents.RecordWorkspaceCleanup(ctx, identity); err != nil {
						return nil, err
					}
					local, remote := net.Pipe()
					go func() {
						defer remote.Close()
						scan := bufio.NewScanner(remote)
						for scan.Scan() {
							var req mcp.Request
							if json.Unmarshal(scan.Bytes(), &req) != nil {
								return
							}
							if len(req.ID) == 0 {
								continue
							}
							if discovery == "initialize-fails" {
								_ = json.NewEncoder(remote).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32603, "message": "initialization refused"}})
								continue
							}
							var result any
							switch req.Method {
							case "initialize":
								result = map[string]any{"protocolVersion": mcp.ProtocolVersion}
							case "tools/list":
								result = map[string]any{"tools": []mcp.ToolDef{{Name: "unselected", InputSchema: map[string]any{"type": "object"}}}}
							}
							if json.NewEncoder(remote).Encode(mcp.Response{JSONRPC: "2.0", ID: req.ID, Result: result}) != nil {
								return
							}
						}
					}()
					return &lifecycleTestStream{ReadWriteCloser: local, closeFn: func() error {
						cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
						defer stop()
						return provisioner.DestroyConfirmed(cleanup, identity)
					}}, nil
				}
				result := runner.Run(o.ctx, run, p.runtimeFor(run))
				if result.State != "failed" {
					t.Fatalf("discovery failure succeeded: %+v", result)
				}
				saved, err := p.agents.GetRun(o.ctx, run.ID)
				if err != nil || saved.WorkspaceIdentity == nil || !saved.WorkspaceIdentity.TerminationObserved || !saved.WorkspaceCleanupPending {
					t.Fatalf("failed namespace cleanup erased: %+v %v", saved, err)
				}
				if !deleteAttempt.Load() {
					t.Fatal("fixture never attempted namespace teardown")
				}
				// Confirmation itself must verify the namespace milestone;
				// even a caller with recorded container termination cannot
				// clear pending after an unrelated/empty session Close.
				if err = p.agents.ConfirmWorkspaceCleanup(o.ctx, run.ID); err == nil {
					t.Fatal("termination evidence alone confirmed namespace teardown")
				}

				if _, err = p.agents.PurgeRuns(o.ctx, &repo); err == nil {
					t.Fatal("purge erased unconfirmed namespace cleanup")
				}
				failing.Store(false)
				if err = p.agents.CleanupRunGrant(o.ctx, run.ID); err != nil {
					t.Fatal(err)
				}
				saved, err = p.agents.GetRun(o.ctx, run.ID)
				if err != nil || saved.WorkspaceCleanupPending {
					t.Fatalf("confirmed retry retained pending: %+v %v", saved, err)
				}
				if _, err = p.agents.PurgeRuns(o.ctx, &repo); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// exitBarrierStream holds delivery of the server's final valid reply until the
// REAL Kubernetes exec has ended and its callback has run. Thus the test never
// relies on a sleep or an extra MCP read to detect transport exit.
type exitBarrierStream struct {
	workspace.StdioLifecycle
	exited chan struct{}
	once   sync.Once
}

func (s *exitBarrierStream) OnExit(fn func()) {
	s.StdioLifecycle.OnExit(func() {
		if fn != nil {
			fn()
		}
		s.once.Do(func() { close(s.exited) })
	})
}
func (s *exitBarrierStream) Read(b []byte) (int, error) {
	n, err := s.StdioLifecycle.Read(b)
	if bytes.Contains(b[:n], []byte(`"replied"`)) {
		select {
		case <-s.exited:
		case <-time.After(20 * time.Second):
			return n, fmt.Errorf("exec exit not observed")
		}
	}
	return n, err
}

func TestRunnerStopsOnRealStdioExitAfterReply(t *testing.T) {
	if os.Getenv("KUBE_CONTEXT") == "" {
		t.Skip("KUBE_CONTEXT not set")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: os.Getenv("KUBE_CONTEXT")}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := newPlatform(t)
	o := p.newOrg(t)
	repo := p.repo(t, o, map[string]string{".novaforge/agents/engineer.yaml": "name: engineer\nrole: engineer\ntools: [mcp.selected.lookup, work.get]\n"})
	run := p.startRun(t, o, repo, "engineer", "read", nil)
	provisioner := workspace.NewProvisioner(client).WithRESTConfig(cfg).WithCleanupRecorder(p.agents.RecordWorkspaceCleanup)
	p.agents.WorkspaceCleaner = provisioner.DestroyConfirmed
	ctx, stop := context.WithTimeout(o.ctx, 2*time.Minute)
	defer stop()
	if _, err = provisioner.Create(ctx, run.ID, workspace.Spec{OrgID: o.id, Image: workspace.DefaultImage, CPULimit: "1", MemLimit: "1Gi"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = provisioner.Destroy(cleanup, run.ID)
	})
	if err = provisioner.WaitReady(ctx, run.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	model := &recordingModel{script: []stubResponse{{toolCalls: []provider.ToolCallPart{toolCall("m1", "mcp.selected.lookup", map[string]any{}), toolCall("other", "work.get", map[string]string{"id": run.WorkItemID.String()})}}, {text: "done"}}}
	var requested []string
	runner := p.runner(model, &requested)
	runner.MCP = stdioRegister{}
	var transport *exitBarrierStream
	runner.MCPOptions.OpenStdio = func(call context.Context, _ string) (io.ReadWriteCloser, error) {
		stream, err := provisioner.OpenStdio(call, run.ID, []string{"sh", "-c", `
read -r line
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}'
read -r line
read -r line
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"lookup","inputSchema":{"type":"object"}}]}}'
read -r line
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"replied"}]}}'
exit 0
`})
		if err != nil {
			return nil, err
		}
		transport = &exitBarrierStream{StdioLifecycle: stream.(workspace.StdioLifecycle), exited: make(chan struct{})}
		transport.OnExit(nil)
		return transport, nil
	}
	result := runner.Run(ctx, run, p.runtimeFor(run))
	if transport == nil {
		t.Fatal("stdio never opened")
	}
	select {
	case <-transport.exited:
	default:
		t.Fatal("real exec did not terminate")
	}
	if len(model.calls) != 1 {
		t.Errorf("model called %d times after actual transport exit, want one", len(model.calls))
	}
	entries, err := p.audit.List(o.ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Tool == "work.get" {
			t.Error("unrelated tool dispatched after actual transport exit")
		}
	}
	if result.State != "failed" {
		t.Errorf("unexpected exit accepted as normal finalization: %+v", result)
	}
	if err = p.agents.CleanupRunGrant(o.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := p.agents.GetRun(o.ctx, run.ID)
	if err != nil || saved.WorkspaceCleanupPending || saved.WorkspaceIdentity == nil || !saved.WorkspaceIdentity.TerminationObserved {
		t.Fatalf("teardown not confirmed: %+v %v", saved, err)
	}
	t.Logf("owned workspace run=%s namespace=nf-run-%s namespace_uid=%s pod_uid=%s termination_observed=%t pending=%t", run.ID, run.ID, saved.WorkspaceIdentity.NamespaceUID, saved.WorkspaceIdentity.PodUID, saved.WorkspaceIdentity.TerminationObserved, saved.WorkspaceCleanupPending)
}
