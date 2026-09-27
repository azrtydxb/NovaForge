package workspace_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/workspace"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// This builds an actual dependency in the production workspace and probes a
// reachable external IP. A policy object alone does not prove enforcement.
func TestVendoredDependencyBuildInIsolatedWorkspace(t *testing.T) {
	kubeContext := os.Getenv("KUBE_CONTEXT")
	if kubeContext == "" {
		t.Skip("source hack/env.sh for the real cluster")
	}
	outside, err := net.DialTimeout("tcp", "1.1.1.1:443", 5*time.Second)
	if err != nil {
		t.Fatalf("external positive control unavailable: %v", err)
	}
	outside.Close()
	dir := t.TempDir()
	files := map[string]string{
		"go.sum": "github.com/google/uuid v1.6.0 h1:NIvaJDMOsjHA8n1jAhLSgzrAzy1Hgr+hNrb57e+94F0=\ngithub.com/google/uuid v1.6.0/go.mod h1:TIyPZe4MgqvfeYDBFedMoGGpEw/LqOeaOT+nhxU+yHo=\n",
		"go.mod": "module fixture.test/offline\n\ngo 1.26\n\nrequire github.com/google/uuid v1.6.0\n",
		"main.go": `package main
import("fmt";"net";"os";"time";"github.com/google/uuid")
func main(){
 if uuid.MustParse("11111111-1111-4111-8111-111111111111").Version()!=4{panic("dependency result")}
 c,e:=net.DialTimeout("tcp","1.1.1.1:443",3*time.Second)
 if e==nil{c.Close();fmt.Println("external network unexpectedly reachable");os.Exit(1)}
 fmt.Println("vendored dependency executed; external TCP denied")
}
`}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "mod", "vendor")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("vendor cached dependency: %v: %s", err, out)
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(name), Mode: 0600, Size: info.Size()}); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := workspace.NewProvisioner(client).WithRESTConfig(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id := uuid.New()
	ws, err := p.Create(ctx, id, workspace.Spec{OrgID: uuid.New(), Image: workspace.DefaultImage, CPULimit: "1", MemLimit: "1Gi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Destroy(context.Background(), id) })
	if err = p.WaitReady(ctx, id, 4*time.Minute); err != nil {
		t.Fatal(err)
	}
	result, err := p.Exec(ctx, id, []string{"tar", "-xf", "-", "-C", workspace.Root}, &archive)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("transfer: %v %s", err, result.Stderr)
	}
	result, err = p.Exec(ctx, id, []string{"sh", "-c", "cd /workspace && GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go build -mod=vendor -o /workspace/offline . && /workspace/offline"}, nil)
	if err != nil || result.ExitCode != 0 || !strings.Contains(string(result.Stdout), "vendored dependency executed; external TCP denied") {
		t.Fatalf("offline build/probe: %v exit=%d stdout=%s stderr=%s", err, result.ExitCode, result.Stdout, result.Stderr)
	}
	t.Logf("namespace=%s image=%s: %s", ws.Namespace, workspace.DefaultImage, result.Stdout)
}
