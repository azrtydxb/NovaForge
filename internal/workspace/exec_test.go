package workspace_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/novaforge/novaforge/internal/workspace"
)

// TestExecInAProvisionedWorkspace provisions a real workspace on the cluster
// named by KUBE_CONTEXT (hack/env.sh), writes a file into it over exec, reads
// it back and runs a command there. The fake clientset cannot exec, and it
// accepted a pod whose container exited immediately — which is how agent
// files came to be staged in agent-runtime's own filesystem instead.
func TestExecInAProvisionedWorkspace(t *testing.T) {
	kubeContext := os.Getenv("KUBE_CONTEXT")
	if kubeContext == "" {
		t.Skip("KUBE_CONTEXT not set; source hack/env.sh to run against the cluster")
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
	p := workspace.NewProvisioner(client).WithRESTConfig(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	runID := uuid.New()
	if _, err := p.Create(ctx, runID, workspace.Spec{Image: workspace.DefaultImage, CPULimit: "1", MemLimit: "1Gi"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = p.Destroy(context.Background(), runID) })
	if err := p.WaitReady(ctx, runID, 4*time.Minute); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	if _, err := p.Exec(ctx, runID, []string{"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", workspace.Root + "/docs/NOTE.md"},
		strings.NewReader("staged in the pod\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := p.Exec(ctx, runID, []string{"cat", workspace.Root + "/docs/NOTE.md"}, nil)
	if err != nil || string(res.Stdout) != "staged in the pod\n" {
		t.Fatalf("read back = %q, %v", res.Stdout, err)
	}
	res, err = p.Exec(ctx, runID, []string{"sh", "-c", "go version && exit 3"}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ExitCode != 3 || !strings.Contains(string(res.Stdout), "go version") {
		t.Fatalf("run = exit %d, stdout %q, stderr %q; want exit 3 with the toolchain present", res.ExitCode, res.Stdout, res.Stderr)
	}
}
